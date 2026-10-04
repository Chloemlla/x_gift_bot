package site

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLegacyDeclineIsNotPresentedAsAutomaticChecking(t *testing.T) {
	s := resumeFixture(t, "review", "unknown")
	b, err := s.vault.Get("checkout:1234")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err = json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	m["submitted_at"] = 124
	m["last_error"] = map[string]any{"HTTP": 402, "Type": "card_error", "Code": "card_declined"}
	b, _ = json.Marshal(m)
	if err = s.vault.Put("checkout:1234", b); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"code": "XG-" + strings.Repeat("A", 48), "username": "recipient"})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/status", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	s.status(w, req)
	var response map[string]any
	if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || response["rechecking"] != false || response["payment_declined"] != true || !strings.Contains(response["message"].(string), "拒绝") {
		t.Fatalf("misleading response: %s", w.Body.String())
	}
}
