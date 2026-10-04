package site

import (
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestManualLinkAdminAndValidationBeforeUpstream(t *testing.T) {
	s := &server{adminHash: sha256.Sum256([]byte("synthetic-admin"))}
	w := httptest.NewRecorder()
	s.admin(s.manualLink)(w, httptest.NewRequest("POST", "/api/admin/manual-link", strings.NewReader(`{"username":"recipient","months":6}`)))
	if w.Code != 401 {
		t.Fatal("unauthenticated generation allowed", w.Code)
	}
	for _, body := range []string{`{"username":"bad user","months":6}`, `{"username":"recipient","months":0}`, `{"username":"recipient","months":25}`, `{"username":"","months":6}`, `{"username":"recipient","months":"6"}`} {
		w = httptest.NewRecorder()
		s.manualLink(w, manualLinkJSONRequest(body))
		if w.Code != 400 {
			t.Fatal("invalid request reached upstream", w.Code)
		}
	}
}
func TestManualLinkPlansAndBusyGuardWhilePublicPaused(t *testing.T) {
	s := resumeFixture(t, "review", "created")
	s.payments = false
	w := httptest.NewRecorder()
	s.manualLinkPlans(w, httptest.NewRequest("GET", "/api/admin/manual-link/plans", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"months":6`) {
		t.Fatal(w.Body.String())
	}
	for _, secret := range []string{"acct_", "prod_", "stripe-key"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("plans leaked internal configuration")
		}
	}
	s.work <- struct{}{}
	defer func() { <-s.work }()
	before, _ := s.vault.Get("checkout:1234")
	w = httptest.NewRecorder()
	s.manualLink(w, manualLinkJSONRequest(`{"username":"@recipient","months":6}`))
	if w.Code != 409 {
		t.Fatal("busy checkout was not protected", w.Code)
	}
	after, _ := s.vault.Get("checkout:1234")
	if string(before) != string(after) {
		t.Fatal("busy request changed order")
	}
}

func manualLinkJSONRequest(body string) *http.Request {
	r := httptest.NewRequest("POST", "/api/admin/manual-link", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}
