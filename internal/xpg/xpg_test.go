package xpg

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xgift/internal/vault"
)

func mkVault(t *testing.T) *vault.Vault {
	t.Helper()
	dir := t.TempDir()
	passwordFile := filepath.Join(dir, "vault-password")
	if err := os.WriteFile(passwordFile, []byte(strings.Repeat("x", 48)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	v, err := vault.Open(filepath.Join(dir, "vault.db"), passwordFile, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { v.Close() })
	return v
}

const testCatalog = `{"merchant":"acct_1Ika5JA3KZ32dPo1","currency":"bdt","plans":[{"months":3,"amount":30000,"product":"prod_TJXJtpzqCpI36N"},{"months":6,"amount":60000,"product":"prod_TJXKKNJwZJIhCM"}]}`

func mkServer(t *testing.T, passcode string) (*server, string) {
	t.Helper()
	v := mkVault(t)
	dir := t.TempDir()
	return newServer(Config{Passcode: passcode, DataDir: dir}, v, 0, nil), dir
}

func doJSON(s *server, method, target, body, key string) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	if key != "" {
		req.Header.Set("X-XPG-Key", key)
	}
	req.Header.Set("X-Real-IP", "203.0.113.7")
	res := httptest.NewRecorder()
	s.routes().ServeHTTP(res, req)
	return res
}

func TestLimiter(t *testing.T) {
	l := newLimiter()
	for i := 0; i < 3; i++ {
		if !l.allow("k", time.Minute, 3) {
			t.Fatalf("allow failed at %d", i)
		}
	}
	if l.allow("k", time.Minute, 3) {
		t.Fatal("expected denial after limit reached")
	}
	if !l.allow("other", time.Minute, 3) {
		t.Fatal("other key should be allowed")
	}
}

func TestAuthorized(t *testing.T) {
	s, _ := mkServer(t, "correct-passcode-12345678")
	r := httptest.NewRequest("POST", "/api/check", strings.NewReader("{}"))
	if s.authorized(r) {
		t.Fatal("missing key must not authorize")
	}
	r.Header.Set("X-XPG-Key", "wrong-passcode-987654321")
	if s.authorized(r) {
		t.Fatal("wrong key must not authorize")
	}
	r.Header.Set("X-XPG-Key", "correct-passcode-12345678")
	if !s.authorized(r) {
		t.Fatal("correct key must authorize")
	}
	open, _ := mkServer(t, "")
	r2 := httptest.NewRequest("POST", "/api/check", strings.NewReader("{}"))
	if !open.authorized(r2) {
		t.Fatal("no configured passcode must authorize by default")
	}
}

func TestCheckValidation(t *testing.T) {
	s, _ := mkServer(t, "")
	res := doJSON(s, "POST", "/api/check", `{"username"}`, "")
	if res.Code != 400 {
		t.Fatalf("expected 400 for invalid body, got %d", res.Code)
	}
	res = doJSON(s, "POST", "/api/check", `{"username":"b@d"}`, "")
	if res.Code != 400 || strings.Contains(res.Body.String(), `"id"`) {
		t.Fatalf("expected 400 for invalid username, got %d: %s", res.Code, res.Body.String())
	}
}

func TestCheckNotReadyWithoutCredentials(t *testing.T) {
	s, _ := mkServer(t, "")
	res := doJSON(s, "POST", "/api/check", `{"username":"mizorewww"}`, "")
	if res.Code != 503 {
		t.Fatalf("expected 503 without vault records, got %d: %s", res.Code, res.Body.String())
	}
	var out struct {
		Error struct{ Code string }
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil || out.Error.Code != "not_ready" {
		t.Fatalf("expected not_ready code, got %s", res.Body.String())
	}
}

func TestPlans(t *testing.T) {
	s, _ := mkServer(t, "")
	if err := s.vault.Put("catalog", []byte(testCatalog)); err != nil {
		t.Fatal(err)
	}
	res := doJSON(s, "GET", "/api/plans", "", "")
	if res.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", res.Code, res.Body.String())
	}
	var out struct {
		Plans []struct {
			Months   int    `json:"months"`
			Amount   int    `json:"amount"`
			Currency string `json:"currency"`
		}
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Plans) != 2 || out.Plans[0].Months != 3 || out.Plans[1].Amount != 60000 || out.Plans[0].Currency != "BDT" {
		t.Fatalf("unexpected plans: %s", res.Body.String())
	}
}

func TestLinkValidation(t *testing.T) {
	s, _ := mkServer(t, "")
	if err := s.vault.Put("catalog", []byte(testCatalog)); err != nil {
		t.Fatal(err)
	}
	res := doJSON(s, "POST", "/api/link", `{"username":"BAD NAME","months":6}`, "")
	if res.Code != 400 {
		t.Fatalf("expected 400 for invalid username, got %d", res.Code)
	}
	res = doJSON(s, "POST", "/api/link", `{"username":"mizorewww","months":12}`, "")
	if res.Code != 400 {
		t.Fatalf("expected 400 for unsupported plan, got %d", res.Code)
	}
}

func TestLinkPasscodeGate(t *testing.T) {
	s, _ := mkServer(t, "correct-passcode-12345678")
	res := doJSON(s, "POST", "/api/link", `{"username":"mizorewww","months":6}`, "wrong-key-98765")
	if res.Code != 401 {
		t.Fatalf("expected 401, got %d", res.Code)
	}
}

func TestOwnerCookieIssuedOnce(t *testing.T) {
	s, _ := mkServer(t, "")
	res := doJSON(s, "GET", "/", "", "")
	cookies := res.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Name != ownerCookie || !validOwnerValue(cookies[0].Value) {
		t.Fatalf("expected owner cookie, got %v", cookies)
	}
	value := cookies[0].Value
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Cookie", ownerCookie+"="+value)
	res2 := httptest.NewRecorder()
	s.ownerCookie(res2, req)
	for _, c := range res2.Result().Cookies() {
		if c.Name == ownerCookie {
			t.Fatal("existing valid owner cookie should not be reissued")
		}
	}
	if got := requestOwner(httptest.NewRecorder(), req); got != value {
		t.Fatalf("requestOwner should return the existing value, got %s", got)
	}
}

func TestHealthzAndAssets(t *testing.T) {
	s, _ := mkServer(t, "")
	res := doJSON(s, "GET", "/healthz", "", "")
	if res.Code != 200 || !strings.Contains(res.Body.String(), `"ok":true`) {
		t.Fatalf("bad healthz: %d %s", res.Code, res.Body.String())
	}
	res = doJSON(s, "GET", "/", "", "")
	if res.Code != 200 || !strings.Contains(res.Body.String(), "Premium 礼品链接生成") {
		t.Fatalf("bad index: %d", res.Code)
	}
	if ct := res.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("bad content type: %s", ct)
	}
	res = doJSON(s, "GET", "/robots.txt", "", "")
	if res.Code != 200 || !strings.Contains(res.Body.String(), "Disallow") {
		t.Fatalf("bad robots: %d", res.Code)
	}
}

func TestParseAnyTLS(t *testing.T) {
	out, err := ParseAnyTLS("anytls://password-secret@example.com:443?peer=sub.example.org&udp=1#%F0%9F%87%A7%F0%9F%87%A9")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Outbounds []struct {
			Type       string `json:"type"`
			Server     string `json:"server"`
			ServerPort int    `json:"server_port"`
			Password   string `json:"password"`
			TLS        struct {
				Enabled    bool   `json:"enabled"`
				ServerName string `json:"server_name"`
				Insecure   bool   `json:"insecure"`
			} `json:"tls"`
		} `json:"outbounds"`
	}
	if err = json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	o := doc.Outbounds[0]
	if o.Type != "anytls" || o.Server != "example.com" || o.ServerPort != 443 || o.Password != "password-secret" {
		t.Fatalf("unexpected outbound: %s", string(out))
	}
	if !o.TLS.Enabled || o.TLS.ServerName != "sub.example.org" || o.TLS.Insecure {
		t.Fatalf("unexpected TLS: %+v", o.TLS)
	}
	if _, err = ParseAnyTLS("https://example.com"); err == nil {
		t.Fatal("expected error for non-anytls link")
	}
}

func TestParseCookieFile(t *testing.T) {
	raw := []byte(`{"cookies":[{"name":"auth_token","value":"aaa","domain":".x.com"},{"name":"ct0","value":"bbb","domain":".x.com"}],"origins":[]}`)
	h, err := ParseCookieFile(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Values("Cookie")) != 2 || h.Get("X-Csrf-Token") != "bbb" {
		t.Fatalf("unexpected headers: %v", h)
	}
	if _, err := ParseCookieFile([]byte(`{"cookies":[{"name":"sessionid","value":"x","domain":".x.com"}]}`)); err == nil {
		t.Fatal("expected error for unexpected cookie name")
	}
	if _, err := ParseCookieFile([]byte(`{"cookies":[]}`)); err == nil {
		t.Fatal("expected error for missing cookies")
	}
}

func TestValidOwnerValue(t *testing.T) {
	if validOwnerValue("not-hex") || validOwnerValue(strings.Repeat("z", 64)) {
		t.Fatal("invalid values must be rejected")
	}
	if !validOwnerValue("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef") {
		t.Fatal("valid hex must pass")
	}
}
