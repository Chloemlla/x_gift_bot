package site

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func lookupRequest(s *server, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/admin/lookup", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.lookup(w, req)
	return w
}

func TestLookupFindsCodeByFullValue(t *testing.T) {
	s := resumeFixture(t, "review", "created")
	if _, err := s.db.Exec("ALTER TABLE codes ADD COLUMN folder_id TEXT"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("ALTER TABLE codes ADD COLUMN copyable INTEGER DEFAULT 1"); err != nil {
		t.Fatal(err)
	}
	w := lookupRequest(s, `{"code":"xg-`+strings.Repeat("a", 48)+`"}`)
	var row codeRow
	if err := json.Unmarshal(w.Body.Bytes(), &row); err != nil {
		t.Fatalf("invalid JSON %q: %v", w.Body.String(), err)
	}
	if w.Code != 200 || row.ID != "code1" || row.Hint != "AAAA" || row.Status != "review" || row.Username != "recipient" || row.Months != 6 || !row.Copyable {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), strings.Repeat("A", 48)) || strings.Contains(w.Body.String(), "1234") {
		t.Fatal("lookup leaked the full code or recipient id")
	}
}

func TestLookupRejectsBadInput(t *testing.T) {
	s := resumeFixture(t, "review", "created")
	if _, err := s.db.Exec("ALTER TABLE codes ADD COLUMN folder_id TEXT"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("ALTER TABLE codes ADD COLUMN copyable INTEGER DEFAULT 1"); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"code":""}`, `{"code":"XG-ABC"}`, `{"code":"` + strings.Repeat("G", 48) + `"}`} {
		if w := lookupRequest(s, body); w.Code != 400 {
			t.Fatalf("body %s: status=%d", body, w.Code)
		}
	}
	if w := lookupRequest(s, `{"code":"XG-`+strings.Repeat("B", 48)+`"}`); w.Code != 404 {
		t.Fatalf("unknown code: status=%d", w.Code)
	}
}

func TestLookupRequiresAdmin(t *testing.T) {
	s := resumeFixture(t, "review", "created")
	req := httptest.NewRequest("POST", "/api/admin/lookup", strings.NewReader(`{"code":"XG-`+strings.Repeat("A", 48)+`"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.admin(s.lookup)(w, req)
	if w.Code != 401 {
		t.Fatalf("unauthenticated lookup: status=%d", w.Code)
	}
}
