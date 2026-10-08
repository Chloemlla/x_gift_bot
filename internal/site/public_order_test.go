package site

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPublicOrderLookupIsScopedToBrowserAndHidesLinks(t *testing.T) {
	s := checkFixture(t)
	owner := strings.Repeat("a", 64)
	sum := sha256.Sum256([]byte(owner))
	put := func(id string, created int64, status string) {
		b, _ := json.Marshal(map[string]any{"owner": hex.EncodeToString(sum[:]), "order": map[string]any{"username": "buyer", "recipient_id": id, "months": 12, "status": status, "created": created, "session_id": "cs_live_" + id, "url": "https://checkout.stripe.com/c/pay/cs_live_" + id}})
		s.vault.Put("public-checkout:"+id, b)
	}
	put("1", time.Now().Add(-time.Hour).Unix(), "succeeded")
	put("2", time.Now().Add(-time.Minute).Unix(), "created")
	lookup := func(cookie, user string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/manual-link/order?username="+user, nil)
		r.AddCookie(&http.Cookie{Name: "__Host-xgift-link", Value: cookie})
		w := httptest.NewRecorder()
		s.publicOrderStatus(w, r)
		return w
	}
	w := lookup(owner, "@Buyer")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"unknown"`) || !strings.Contains(w.Body.String(), `"months":12`) || strings.Contains(w.Body.String(), "cs_live") {
		t.Fatal("latest order wrong or link leaked", w.Code, w.Body.String())
	}
	if w = lookup(strings.Repeat("b", 64), "buyer"); w.Code != 404 {
		t.Fatal("another browser saw the order", w.Code)
	}
	if w = lookup(owner, "someone"); w.Code != 404 {
		t.Fatal("unrelated username matched", w.Code)
	}
}
