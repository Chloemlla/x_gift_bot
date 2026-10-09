package checkout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestExplicitPublicRegenerationCreatesFreshSession(t *testing.T) {
	for _, state := range []string{"open", "old_open", "expired", "inactive", "change_plan", "creating", "paid", "stale_paid", "other_owner", "same_request", "history_paid", "newer_paid", "upstream_replay"} {
		t.Run(state, func(t *testing.T) {
			v := controlFixture(t)
			plan := Plan{Months: 3, Minor: 30000, Currency: "usd", Merchant: "acct_Test", ProductID: "prod_Test"}
			owner := strings.Repeat("a", 64)
			sum := sha256.Sum256([]byte(owner))
			r := Record{Username: "recipient", RecipientID: "1234", Months: 3, Amount: 30000, Currency: "USD", ProductID: plan.ProductID, SessionID: "cs_live_Old", URL: "https://checkout.stripe.com/a/pay/cs_live_Old", Status: "created", Created: time.Now().Unix()}
			if state == "old_open" {
				r.Created = 1
			}
			if state == "creating" {
				r.Status = "creating"
				r.SessionID = ""
				r.URL = ""
			}
			saved := publicLinkRecord{Owner: hex.EncodeToString(sum[:]), Order: r}
			if state == "other_owner" {
				saved.Owner = "other"
			}
			if state == "stale_paid" {
				r.Status = "succeeded"
				r.Created = 1
				saved.Order = r
			}
			if state == "history_paid" {
				old := r
				old.SessionID = "cs_live_History"
				old.URL = "https://checkout.stripe.com/a/pay/cs_live_History"
				saved.History = []Record{old}
			}
			if state == "same_request" {
				saved.RequestID = "request"
			}
			raw, _ := json.Marshal(saved)
			if err := v.Put("public-checkout:1234", raw); err != nil {
				t.Fatal(err)
			}
			if state == "change_plan" {
				plan.Months = 6
				plan.Minor = 60000
				plan.ProductID = "prod_New"
			}
			mutations := 0
			client := &http.Client{Transport: stripeRoundTrip(func(req *http.Request) (*http.Response, error) {
				var body string
				switch {
				case req.Method == "GET" && strings.HasSuffix(req.URL.Path, "/PremiumGiftingQuery"):
					body = `{"data":{"user":{"result":{"rest_id":"1234","premium_gifting_eligible":true,"core":{"screen_name":"recipient"}}}}}`
				case req.Method == "GET" && strings.HasSuffix(req.URL.Path, "/useSubscriptionProductDetailsByRestIdQuery"):
					body = fmt.Sprintf(`{"data":{"web_subscription_product_details_by_rest_id":{"rest_id":%q,"prices":[{"price_type":"OneTime","currency_code":"usd","amount_local_micro":%d}]}}}`, plan.ProductID, int64(plan.Minor)*10000)
				case req.Method == "POST" && strings.HasSuffix(req.URL.Path, "/useOneTimePurchaseGiftMutation"):
					mutations++
					body = `{"data":{"onetimepurchase_gift":{"session_id":"cs_live_New","session_url":"https://checkout.stripe.com/a/pay/cs_live_New","session_status":"Unpaid"}}}`
				default:
					t.Fatalf("unexpected API or charge operation: %s %s", req.Method, req.URL.Path)
				}
				if state == "upstream_replay" && req.Method == "POST" {
					body = strings.ReplaceAll(body, "cs_live_New", "cs_live_Old")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			x := &xClient{vault: v, headers: http.Header{}, http: client, regionalHTTP: client, publicRequestID: "request", readCheckout: func(_ context.Context, order *Record) (*paymentPage, error) {
				p := plan
				if order.SessionID == r.SessionID {
					p = Plan{Months: r.Months, Minor: r.Amount, Currency: "usd", ProductID: r.ProductID, Merchant: plan.Merchant}
				}
				page := publicPageFixture(order, p)
				if order.SessionID == r.SessionID {
					switch state {
					case "inactive":
						return nil, &stripeError{Code: "checkout_not_active_session"}
					case "expired":
						page.Status = "expired"
					case "paid":
						page.Status = "complete"
						page.PaymentStatus = "paid"
					}
				}
				if state == "history_paid" && order.SessionID == "cs_live_History" || state == "newer_paid" && order.SessionID == "cs_live_Old" {
					page.Status = "complete"
					page.PaymentStatus = "paid"
				}
				return page, nil
			}}
			got, err := publicLinkForClient(context.Background(), v, "recipient", owner, plan, x)
			if state == "other_owner" {
				// A different browser adopts the chain and generates its own session;
				// it never receives the earlier link, only its payment evidence.
				if err != nil || mutations != 1 || got.SessionID != "cs_live_New" {
					t.Fatal("cross-browser regeneration blocked", err)
				}
				raw, _ := v.Get("public-checkout:1234")
				var after publicLinkRecord
				json.Unmarshal(raw, &after)
				if after.Owner != hex.EncodeToString(sum[:]) || len(after.History) != 1 {
					t.Fatal("order ownership or history lost during adoption")
				}
				return
			}
			if state == "upstream_replay" {
				if err == nil || mutations != 1 {
					t.Fatal("upstream replay labeled fresh")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if state == "paid" || state == "history_paid" || state == "newer_paid" {
				if got.Status != "succeeded" || mutations != 0 {
					t.Fatal("paid checkout recreated")
				}
				return
			}
			if state == "stale_paid" {
				// Long-settled orders must not veto a later purchase.
				if got.SessionID != "cs_live_New" || mutations != 1 {
					t.Fatal("stale paid order blocked a new purchase", err)
				}
				raw, _ = v.Get("public-checkout:1234")
				var after publicLinkRecord
				json.Unmarshal(raw, &after)
				if len(after.History) != 1 || after.History[0].Status != "succeeded" {
					t.Fatal("settled order history lost")
				}
				return
			}
			if state == "same_request" {
				if got.SessionID != r.SessionID || mutations != 0 {
					t.Fatal("request replayed")
				}
				return
			}
			if got.SessionID != "cs_live_New" || mutations != 1 {
				t.Fatal("fresh session missing")
			}
			raw, _ = v.Get("public-checkout:1234")
			var after publicLinkRecord
			json.Unmarshal(raw, &after)
			if len(after.History) != 1 || after.History[0].SessionID != r.SessionID || after.Order.SessionID != got.SessionID {
				t.Fatal("history lost")
			}
			again, e := publicLinkForClient(context.Background(), v, "recipient", owner, plan, x)
			if e != nil || again.SessionID != got.SessionID || mutations != 1 {
				t.Fatal("same request created twice", e)
			}
		})
	}
}

func TestHistoricalPublicPaymentDoesNotMarkNewSessionPaid(t *testing.T) {
	v := controlFixture(t)
	old := Record{RecipientID: "1234", SessionID: "cs_live_Old", Status: "created"}
	newer := Record{RecipientID: "1234", SessionID: "cs_live_New", Status: "created"}
	raw, _ := json.Marshal(publicLinkRecord{Owner: "owner", Order: newer, History: []Record{old}})
	v.Put("public-checkout:1234", raw)
	if err := RecordPublicOrderPaid(v, &old); err != nil {
		t.Fatal(err)
	}
	raw, _ = v.Get("public-checkout:1234")
	var saved publicLinkRecord
	json.Unmarshal(raw, &saved)
	if saved.Order.Status != "created" || saved.Order.SessionID != newer.SessionID || saved.History[0].Status != "succeeded" || saved.Owner != "owner" {
		t.Fatal("old payment misattributed")
	}
	if err := RecordPublicOrderPaid(v, &newer); err != nil {
		t.Fatal(err)
	}
	raw, _ = v.Get("public-checkout:1234")
	json.Unmarshal(raw, &saved)
	if saved.Order.Status != "succeeded" || saved.History[0].Status != "succeeded" {
		t.Fatal("separate payments not retained")
	}
}

func TestUnknownElapsedWindowAllowsOnlyExplicitLinkCreation(t *testing.T) {
	for _, public := range []bool{false, true} {
		v := controlFixture(t)
		old := Record{RecipientID: "old", SessionID: "cs_live_Old", Status: "created", Created: 1}
		// Explicit public requests release any elapsed window; automatic paths
		// retain payment protection while an undecidable window is inside grace.
		expiresAt := int64(1)
		if !public {
			// readActiveCheckout derives the window from the order's creation.
			old.Created = time.Now().Add(-4 * time.Minute).Unix()
			expiresAt = time.Now().Add(-time.Minute).UnixMilli()
		}
		if err := saveActiveCheckout(v, activeCheckout{Order: old, Plan: Plan{}, ExpiresAt: expiresAt}); err != nil {
			t.Fatal(err)
		}
		x := &xClient{vault: v, publicGeneration: public, readCheckout: func(context.Context, *Record) (*paymentPage, error) {
			return nil, &stripeError{Code: "checkout_not_active_session"}
		}}
		err := x.checkCreation(context.Background(), time.Now())
		if public && err != nil {
			t.Fatal("unknown old window blocked explicit generation", err)
		}
		if !public && err == nil {
			t.Fatal("automatic path lost payment protection")
		}
		a, e := readActiveCheckout(v)
		if e != nil || a.Released != public || a.Order.Status != "created" {
			t.Fatal("payment incorrectly declared unpaid/paid", e)
		}
	}
}

func TestPublicSuccessCannotBeDowngradedByStaleRead(t *testing.T) {
	v := controlFixture(t)
	current := Record{RecipientID: "1234", SessionID: "cs_live_Current", Status: "succeeded"}
	prior := Record{RecipientID: "1234", SessionID: "cs_live_Prior", Status: "succeeded"}
	raw, _ := json.Marshal(publicLinkRecord{Order: current, History: []Record{prior}})
	v.Put("public-checkout:1234", raw)
	current.Status = "created"
	prior.Status = "created"
	if err := persistPublicVerification(v, &current); err != nil {
		t.Fatal(err)
	}
	if err := persistPublicVerification(v, &prior); err != nil {
		t.Fatal(err)
	}
	raw, _ = v.Get("public-checkout:1234")
	var saved publicLinkRecord
	json.Unmarshal(raw, &saved)
	if saved.Order.Status != "succeeded" || saved.History[0].Status != "succeeded" {
		t.Fatal("paid state downgraded")
	}
}
