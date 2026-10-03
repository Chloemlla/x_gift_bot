package site

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCustomerLookupReturnsBoundCodeAndSingleOrderPreview(t *testing.T) {
	s := recoveryFixture(t, "created")
	id := strings.Repeat("a", 32)
	if _, err := s.db.Exec("ALTER TABLE codes ADD COLUMN copyable INTEGER DEFAULT 1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE codes SET id=?", id); err != nil {
		t.Fatal(err)
	}
	code := "XG-" + strings.Repeat("A", 48)
	s.vault.Put("redemption:"+id, []byte(code))
	w := httptest.NewRecorder()
	s.customerOrder(w, httptest.NewRequest("GET", "/api/admin/customer?username=%40RECIPIENT", nil))
	var result struct {
		Code       string  `json:"code"`
		CanRecover bool    `json:"can_recover"`
		Order      codeRow `json:"order"`
	}
	json.Unmarshal(w.Body.Bytes(), &result)
	if w.Code != 200 || result.Code != code || result.Order.Username != "recipient" || !result.CanRecover {
		t.Fatal(w.Body.String())
	}
	s.db.Exec("INSERT INTO codes(id,username,recipient_id,months,status,hint) VALUES('other','other','9999',6,'review','OTHER')")
	w = recoveryRequest(s, "preview", `{"id":"`+id+`","mode":"links"}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	q, _ := s.loadRecovery()
	if q.Mode != "links" || len(q.Items) != 1 || q.Items[0].ID != id {
		t.Fatal("single customer preview included other orders")
	}
	if len(s.work) != 0 {
		t.Fatal("preview started work")
	}
	s.vault.Put("redemption:"+id, []byte("XG-"+strings.Repeat("B", 48)))
	w = httptest.NewRecorder()
	s.customerOrder(w, httptest.NewRequest("GET", "/api/admin/customer?id="+id, nil))
	if w.Code != 503 || strings.Contains(w.Body.String(), strings.Repeat("B", 48)) {
		t.Fatal("mismatched code leaked")
	}
}
func TestCustomerEndpointRequiresAdmin(t *testing.T) {
	s := recoveryFixture(t, "created")
	w := httptest.NewRecorder()
	s.admin(s.customerOrder)(w, httptest.NewRequest("GET", "/api/admin/customer?username=recipient", nil))
	if w.Code != 401 || strings.Contains(w.Body.String(), "XG-") {
		t.Fatal("unauthenticated lookup allowed")
	}
}
