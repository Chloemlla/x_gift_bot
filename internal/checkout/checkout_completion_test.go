package checkout

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestVerifiedPublicCheckoutCompletion(t *testing.T) {
	cases := []string{"paid", "pending", "requires_action", "inactive", "wrong_session", "wrong_recipient", "wrong_amount", "wrong_currency", "wrong_merchant", "sandbox", "missing_sandbox", "test_mode", "subscription", "one_success_field", "no_proof", "bad_proof", "wrong_product"}
	for _, kind := range cases {
		t.Run(kind, func(t *testing.T) {
			v := controlFixture(t)
			p := Plan{Months: 6, Minor: 60000, Currency: "usd", ProductID: "prod_Test", Merchant: "acct_Test"}
			r := &Record{Username: "recipient", RecipientID: "1234", Months: 6, Amount: 60000, Currency: "USD", ProductID: p.ProductID, SessionID: "cs_live_Test", URL: "https://checkout.stripe.com/c/pay/cs_live_Test", Status: "created"}
			page := publicPageFixture(r, p)
			if kind != "no_proof" {
				if err := rememberVerifiedCheckout(v, r, p, page); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "bad_proof" {
				page.Account.ID = "acct_Other"
				b, _ := json.Marshal(page)
				v.Put("checkout-verification:"+r.SessionID, b)
			}
			if kind == "wrong_product" {
				r.ProductID = "prod_Other"
			}
			calls := 0
			s := &stripeClient{vault: v, key: "pk_live_Test", http: &http.Client{Transport: stripeRoundTrip(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != "GET" || !strings.HasSuffix(req.URL.Path, "/poll") {
					t.Fatalf("not a read-only result request: %s %s", req.Method, req.URL.Path)
				}
				data := map[string]any{"session_id": r.SessionID, "livemode": true, "is_sandbox_merchant": false, "mode": "payment", "success_url": "https://x.com/recipient/gift-premium/success", "state": "succeeded", "payment_object_status": "succeeded"}
				status := 200
				switch kind {
				case "pending":
					data["state"] = "pending"
					data["payment_object_status"] = "processing"
				case "requires_action":
					data["state"] = "active"
					data["payment_object_status"] = "requires_action"
				case "inactive":
					status = 400
					data = map[string]any{"error": map[string]string{"code": "checkout_not_active_session"}}
				case "wrong_session":
					data["session_id"] = "cs_live_Other"
				case "wrong_recipient":
					data["success_url"] = "https://x.com/other/gift-premium/success"
				case "wrong_amount":
					data["amount"] = 1
				case "wrong_currency":
					data["currency"] = "eur"
				case "wrong_merchant":
					data["account_id"] = "acct_Other"
				case "sandbox":
					data["is_sandbox_merchant"] = true
				case "missing_sandbox":
					delete(data, "is_sandbox_merchant")
				case "test_mode":
					data["livemode"] = false
				case "subscription":
					data["mode"] = "subscription"
				case "one_success_field":
					data["payment_object_status"] = "processing"
				}
				b, _ := json.Marshal(data)
				return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(b)))}, nil
			})}}
			paid, err := s.verifiedCheckoutPaid(context.Background(), r, p)
			if paid != (kind == "paid") {
				t.Fatalf("paid=%t for %s: %v", paid, kind, err)
			}
			if kind == "paid" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err = v.Get("checkout-completion:" + r.SessionID); err != nil {
					t.Fatal("missing completion evidence", err)
				}
			}
			if (kind == "no_proof" || kind == "bad_proof" || kind == "wrong_product") && calls != 0 {
				t.Fatal("unverified binding reached result endpoint")
			}
		})
	}
}
