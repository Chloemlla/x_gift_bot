package checkout

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestBrowserReadOnlySuccessGuards(t *testing.T) {
	for _, tc := range []string{"paid", "open", "expired", "missing_intent", "wrong_session", "wrong_merchant", "wrong_amount", "wrong_currency", "wrong_product", "wrong_return", "wrong_received", "pending"} {
		t.Run(tc, func(t *testing.T) {
			v, r, plan, proof := manualFixture(t)
			var page map[string]any
			json.Unmarshal(proof.Page, &page)
			page["status"] = "complete"
			page["payment_status"] = "paid"
			intent := page["payment_intent"].(map[string]any)
			intent["status"] = "succeeded"
			intent["amount_received"] = plan.Minor
			switch tc {
			case "open", "expired":
				page["status"] = tc
				page["payment_status"] = "unpaid"
				page["payment_intent"] = nil
			case "missing_intent":
				delete(page, "payment_intent")
			case "wrong_session":
				page["session_id"] = "cs_live_Other"
			case "wrong_merchant":
				page["account_settings"].(map[string]any)["account_id"] = "acct_Other"
			case "wrong_amount":
				page["total_summary"].(map[string]any)["total"] = 1
			case "wrong_currency":
				page["currency"] = "eur"
			case "wrong_product":
				page["line_item_group"].(map[string]any)["line_items"].([]any)[0].(map[string]any)["price"].(map[string]any)["product"].(map[string]any)["id"] = "prod_Other"
			case "wrong_return":
				page["success_url"] = "https://x.com/other/gift-premium/success"
			case "wrong_received":
				intent["amount_received"] = 1
			case "pending":
				page["status"] = "open"
				page["payment_status"] = "unpaid"
				intent["status"] = "processing"
			}
			body, _ := json.Marshal(page)
			calls := 0
			s := &stripeClient{vault: v, key: "pk_live_Test", http: &http.Client{Transport: stripeRoundTrip(func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" || !strings.HasSuffix(req.URL.Path, "/payment_pages/cs_live_Test") {
					t.Fatal("non-read payment request")
				}
				calls++
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})}}
			paid, err := s.verifiedCheckoutPaid(context.Background(), r, plan)
			if calls != 1 {
				t.Fatal("unexpected requests")
			}
			if tc == "paid" {
				if !paid || err != nil {
					t.Fatal("browser payment missed")
				}
			} else if paid {
				t.Fatal("unverified success accepted")
			}
			if tc != "paid" && tc != "open" && tc != "expired" && tc != "pending" && err == nil {
				t.Fatal("mismatch not rejected")
			}
		})
	}
}

func TestInactiveLinkedIntentNeverCreatesOrConfirms(t *testing.T) {
	for _, tc := range []string{"success", "missing_secret", "wrong_intent", "wrong_received", "pending", "wrong_merchant"} {
		t.Run(tc, func(t *testing.T) {
			v, r, plan, proof := manualFixture(t)
			raw := string(proof.Page)
			if tc == "missing_secret" {
				raw = strings.ReplaceAll(raw, "pi_Test_secret_Synthetic", "")
			}
			if tc == "wrong_merchant" {
				raw = strings.ReplaceAll(raw, "acct_Test", "acct_Other")
			}
			v.Put("checkout-verification:"+r.SessionID, []byte(raw))
			calls := 0
			s := &stripeClient{vault: v, key: "pk_live_Test", http: &http.Client{Transport: stripeRoundTrip(func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" {
					t.Fatal("non-read request")
				}
				calls++
				code := 400
				body := `{"error":{"type":"invalid_request_error","code":"checkout_not_active_session","message":"inactive"}}`
				if strings.HasSuffix(req.URL.Path, "/payment_intents/pi_Test") {
					code = 200
					id := "pi_Test"
					received := plan.Minor
					state := "succeeded"
					if tc == "wrong_intent" {
						id = "pi_Other"
					}
					if tc == "wrong_received" {
						received = 1
					}
					if tc == "pending" {
						state = "processing"
					}
					b, _ := json.Marshal(map[string]any{"id": id, "status": state, "currency": "usd", "amount": plan.Minor, "amount_received": received, "livemode": true})
					body = string(b)
				} else if !strings.HasSuffix(req.URL.Path, "/poll") {
					t.Fatal("unexpected endpoint")
				}
				return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}}
			paid, err := s.verifiedCheckoutPaid(context.Background(), r, plan)
			if tc == "success" {
				if !paid || err != nil || calls != 2 {
					t.Fatal("linked success missed")
				}
			} else if paid || err == nil {
				t.Fatal("unverified linked result accepted")
			}
		})
	}
}

func TestCheckoutLinkBlocksCompletedAndUnknown(t *testing.T) {
	r := &Record{SessionID: "cs_live_Test", URL: "https://checkout.stripe.com/a/pay/cs_live_Test"}
	for _, status := range []string{"succeeded", "unknown", "submitting", "declined", "creating"} {
		r.Status = status
		if CheckoutLink(r) != "" {
			t.Fatal("unsafe link exported")
		}
	}
	r.Status = "created"
	r.LinkBlocked = true
	if CheckoutLink(r) != "" {
		t.Fatal("known stale link exported")
	}
	r.LinkBlocked = false
	if CheckoutLink(r) == "" {
		t.Fatal("valid created link hidden")
	}
}

func TestPublicExistingUnknownOrStaleNeverRecreates(t *testing.T) {
	for _, tc := range []string{"inactive", "expired", "old_open", "creating"} {
		t.Run(tc, func(t *testing.T) {
			v := controlFixture(t)
			plan := Plan{Months: 3, Minor: 30000, Currency: "usd", Merchant: "acct_Test", ProductID: "prod_Test"}
			r := Record{Username: "recipient", RecipientID: "1234", Months: 3, Amount: 30000, Currency: "USD", ProductID: "prod_Test", SessionID: "cs_live_Test", URL: "https://checkout.stripe.com/a/pay/cs_live_Test", Status: "created", Created: 1}
			if tc == "creating" {
				r.Status = "creating"
				r.SessionID = ""
				r.URL = ""
			}
			before, _ := json.Marshal(publicLinkRecord{Owner: "original-owner", Order: r})
			v.Put("public-checkout:1234", before)
			identityReads := 0
			x := &xClient{vault: v, headers: http.Header{}, http: &http.Client{Transport: stripeRoundTrip(func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" || !strings.HasSuffix(req.URL.Path, "/PremiumGiftingQuery") {
					t.Fatal("existing order attempted creation or unexpected API")
				}
				identityReads++
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"data":{"user":{"result":{"rest_id":"1234","premium_gifting_eligible":true,"core":{"screen_name":"recipient"}}}}}`))}, nil
			})}, readCheckout: func(context.Context, *Record) (*paymentPage, error) {
				if tc == "inactive" {
					return nil, &stripeError{Code: "checkout_not_active_session"}
				}
				page := publicPageFixture(&r, plan)
				if tc == "expired" {
					page.Status = "expired"
				}
				return page, nil
			}}
			_, err := publicLinkForClient(context.Background(), v, "recipient", strings.Repeat("a", 64), plan, x)
			if err == nil || identityReads != 1 {
				t.Fatal("unsafe existing order accepted")
			}
			b, _ := v.Get("public-checkout:1234")
			var after publicLinkRecord
			json.Unmarshal(b, &after)
			if after.Order.SessionID != r.SessionID || after.Order.Created != r.Created || after.Owner != "original-owner" {
				t.Fatal("session or history overwritten")
			}
		})
	}
}
