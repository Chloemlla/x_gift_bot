package site

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPausedRedemptionStopsBeforeOrderOrUpstreamAccess(t *testing.T) {
	// No database, vault, or worker is available: a paused request must return
	// immediately without inspecting an order or starting an upstream request.
	s := &server{payments: false}
	r := httptest.NewRequest(http.MethodPost, "/api/redeem", strings.NewReader(`{"code":"XG-`+strings.Repeat("A", 48)+`","username":"recipient"}`))
	w := httptest.NewRecorder()
	s.redeem(w, r)
	var body struct{ Status, Message string }
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusServiceUnavailable || body.Status != "paused" || !strings.Contains(body.Message, "充值暂时暂停") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
