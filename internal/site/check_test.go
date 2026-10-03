package site

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"xgift/internal/checkout"
	"xgift/internal/vault"
)

func checkFixture(t *testing.T) *server {
	t.Helper()
	dir := t.TempDir()
	password := filepath.Join(dir, "password")
	if e := os.WriteFile(password, []byte(strings.Repeat("p", 32)), 0600); e != nil {
		t.Fatal(e)
	}
	v, e := vault.Open(filepath.Join(dir, "vault.db"), password, true)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { v.Close() })
	return &server{vault: v, origin: "https://example.test", work: make(chan struct{}, 1), limits: map[string]limit{}}
}

func fakeEligibility(t *testing.T, calls *int, id string, err error) {
	t.Helper()
	original := eligibilityCheck
	eligibilityCheck = func(context.Context, *vault.Vault, string, int) (string, error) {
		*calls++
		return id, err
	}
	t.Cleanup(func() { eligibilityCheck = original })
}

func submitCheck(s *server, user string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"username": user})
	req := httptest.NewRequest("POST", "/api/check", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.check(w, req)
	return w
}

func checkBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if e := json.Unmarshal(w.Body.Bytes(), &body); e != nil {
		t.Fatalf("invalid JSON %q: %v", w.Body.String(), e)
	}
	return body
}

func TestEligibilityCheck(t *testing.T) {
	t.Run("eligible account", func(t *testing.T) {
		s := checkFixture(t)
		calls := 0
		fakeEligibility(t, &calls, "1234", nil)
		w := submitCheck(s, "someone")
		body := checkBody(t, w)
		if w.Code != 200 || body["eligible"] != true || calls != 1 {
			t.Fatalf("status=%d body=%s calls=%d", w.Code, w.Body.String(), calls)
		}
		if len(s.work) != 0 {
			t.Fatal("worker slot leaked")
		}
	})
	t.Run("account cannot receive gifts", func(t *testing.T) {
		s := checkFixture(t)
		calls := 0
		fakeEligibility(t, &calls, "", checkout.ErrNotEligible)
		w := submitCheck(s, "someone")
		body := checkBody(t, w)
		msg, _ := body["message"].(string)
		if w.Code != 200 || body["eligible"] != false || !strings.Contains(msg, "不允许") {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	})
	t.Run("account not found", func(t *testing.T) {
		s := checkFixture(t)
		calls := 0
		fakeEligibility(t, &calls, "", checkout.ErrUserNotFound)
		w := submitCheck(s, "ghost")
		body := checkBody(t, w)
		msg, _ := body["message"].(string)
		if w.Code != 200 || body["eligible"] != false || !strings.Contains(msg, "未能找到") {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	})
	t.Run("x failure is never guessed as ineligible", func(t *testing.T) {
		s := checkFixture(t)
		calls := 0
		fakeEligibility(t, &calls, "", errors.New("X PremiumGiftingQuery returned HTTP 500"))
		w := submitCheck(s, "someone")
		msg, _ := checkBody(t, w)["message"].(string)
		if w.Code != 503 || !strings.Contains(msg, "检测") {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		if len(s.work) != 0 {
			t.Fatal("worker slot leaked")
		}
	})
	t.Run("invalid username rejected before any X call", func(t *testing.T) {
		s := checkFixture(t)
		calls := 0
		fakeEligibility(t, &calls, "1234", nil)
		for _, user := range []string{"", "Bad Name", "toolongusername123456", "-root"} {
			if w := submitCheck(s, user); w.Code != 400 {
				t.Fatalf("username %q: status=%d", user, w.Code)
			}
		}
		if calls != 0 {
			t.Fatalf("X called %d times for invalid input", calls)
		}
	})
	t.Run("busy worker returns 503 instead of queueing", func(t *testing.T) {
		s := checkFixture(t)
		calls := 0
		fakeEligibility(t, &calls, "1234", nil)
		s.work <- struct{}{}
		defer func() { <-s.work }()
		w := submitCheck(s, "someone")
		msg, _ := checkBody(t, w)["message"].(string)
		if w.Code != 503 || !strings.Contains(msg, "检测") || calls != 0 {
			t.Fatalf("status=%d body=%s calls=%d", w.Code, w.Body.String(), calls)
		}
	})
	t.Run("check has its own rate limit bucket", func(t *testing.T) {
		s := checkFixture(t)
		ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
		h := s.middleware(ok)
		post := func(path string) int {
			req := httptest.NewRequest("POST", path, strings.NewReader("{}"))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", s.origin)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			return w.Code
		}
		for i := 0; i < 8; i++ {
			if code := post("/api/check"); code != 204 {
				t.Fatalf("request %d: status=%d", i+1, code)
			}
		}
		if code := post("/api/check"); code != 429 {
			t.Fatalf("ninth check: status=%d", code)
		}
		// The general API bucket is unaffected by an exhausted check bucket.
		if code := post("/api/status"); code != 204 {
			t.Fatalf("status after check limit: status=%d", code)
		}
	})
}
