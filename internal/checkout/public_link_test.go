package checkout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestPublicLinkOwnershipAndPaymentIsolation(t *testing.T) {
	plan := Plan{Months: 6, Minor: 60000, Currency: "usd", ProductID: "prod_TEST6MO"}
	base := Record{Username: "recipient", RecipientID: "1234", Months: 6, Amount: 60000, Currency: "USD", ProductID: plan.ProductID, Status: "created", SessionID: "cs_live_TestPublic123", URL: "https://checkout.stripe.com/c/pay/cs_live_TestPublic123"}
	for _, tc := range []struct {
		name    string
		owner   string
		change  func(*Record)
		private bool
		allowed bool
	}{
		{"own public link", "owner", func(r *Record) {}, false, true},
		{"other browser", "other", func(r *Record) {}, false, true},
		{"stored card", "owner", func(r *Record) { r.CardFingerprint = "private-card" }, false, false},
		{"payment method", "owner", func(r *Record) { r.PaymentMethod = "pm_private" }, false, false},
		{"submitted", "owner", func(r *Record) { r.SubmittedAt = 123 }, false, false},
		{"ambiguous creation", "owner", func(r *Record) { r.Status = "creating" }, false, false},
		{"different plan to verify before replacement", "owner", func(r *Record) { r.Months = 3 }, false, true},
		{"admin order", "owner", func(r *Record) {}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := controlFixture(t)
			r := base
			tc.change(&r)
			b, _ := json.Marshal(publicLinkRecord{Owner: "owner", Order: r})
			v.Put("public-checkout:1234", b)
			if tc.private {
				v.Put("checkout:1234", []byte(`{"status":"requires_action","payment_method":"pm_private"}`))
			}
			got, err := publicLinkExisting(v, "recipient", "1234", plan)
			if tc.allowed {
				if err != nil || got == nil {
					t.Fatal(err)
				}
			} else if got != nil || !errors.Is(err, ErrPublicLinkConflict) {
				t.Fatalf("private or ambiguous checkout exposed: %v", err)
			}
			after, _ := v.Get("public-checkout:1234")
			if string(after) != string(b) {
				t.Fatal("read changed checkout")
			}
		})
	}
}

func publicPageFixture(r *Record, plan Plan) *paymentPage {
	raw := fmt.Sprintf(`{"session_id":%q,"currency":%q,"mode":"payment","livemode":true,"status":"open","payment_status":"unpaid","init_checksum":"verified","success_url":%q,"cancel_url":%q,"account_settings":{"account_id":%q},"total_summary":{"due":%d,"subtotal":%d,"total":%d},"line_item_group":{"currency":%q,"due":%d,"subtotal":%d,"total":%d,"line_items":[{"name":%q,"quantity":1,"subtotal":%d,"total":%d,"price":{"currency":%q,"type":"one_time","unit_amount":%d,"product":{"id":%q,"name":%q,"livemode":true}}}]},"payment_intent":null}`, r.SessionID, plan.Currency, "https://x.com/"+r.Username+"/gift-premium/success", "https://x.com/"+r.Username+"/gift-premium", plan.Merchant, plan.Minor, plan.Minor, plan.Minor, plan.Currency, plan.Minor, plan.Minor, plan.Minor, plan.Name(), plan.Minor, plan.Minor, plan.Currency, plan.Minor, plan.ProductID, plan.Name())
	var p paymentPage
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		panic(err)
	}
	return &p
}

func TestPublicCheckoutVerificationRejectsWrongOrInactiveOrders(t *testing.T) {
	plan := Plan{Months: 3, Minor: 30000, Currency: "usd", ProductID: "prod_Test", Merchant: "acct_Test"}
	r := Record{Username: "recipient", RecipientID: "1234", SessionID: "cs_live_Test"}
	for _, tc := range []struct {
		name   string
		change func(*paymentPage)
		err    error
	}{
		{"session", func(p *paymentPage) { p.SessionID = "cs_live_Other" }, nil},
		{"recipient", func(p *paymentPage) { p.SuccessURL = "https://x.com/other/gift-premium/success" }, nil},
		{"merchant", func(p *paymentPage) { p.Account.ID = "acct_Other" }, nil},
		{"amount", func(p *paymentPage) { p.Total.Total++ }, nil},
		{"product", func(p *paymentPage) { p.Group.Items[0].Price.Product.ID = "prod_Other" }, nil},
		{"currency", func(p *paymentPage) { p.Currency = "eur" }, nil},
		{"missing payment intent evidence", func(p *paymentPage) { p.IntentPresent = false }, nil},
		{"expired", func(p *paymentPage) { p.Status = "expired" }, ErrVerifyUnpaid},
		{"inactive", func(p *paymentPage) {}, &stripeError{Code: "checkout_not_active_session"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := controlFixture(t)
			p := publicPageFixture(&r, plan)
			tc.change(p)
			x := &xClient{readCheckout: func(context.Context, *Record) (*paymentPage, error) {
				if tc.name == "inactive" {
					return nil, tc.err
				}
				return p, nil
			}}
			err := verifyPublicCheckout(context.Background(), v, x, &r, plan)
			if err == nil {
				t.Fatal("unverified order accepted")
			}
			if tc.name == "inactive" && !errors.Is(err, ErrPublicPaymentInProgress) {
				t.Fatal(err)
			}
			if tc.name == "expired" && !errors.Is(err, ErrVerifyUnpaid) {
				t.Fatal(err)
			}
		})
	}
}

func TestPublicIdleIntentRejectsExplicitFundsAndProcessing(t *testing.T) {
	for _, raw := range []string{
		`{"payment_status":"unpaid","payment_intent":{"status":"requires_payment_method","amount_received":1}}`,
		`{"payment_status":"unpaid","payment_intent":{"status":"requires_payment_method","amount_capturable":1}}`,
		`{"payment_status":"unpaid","payment_intent":{"status":"processing"}}`,
		`{"payment_status":"unpaid","payment_intent":{"status":"requires_action"}}`,
		`{"payment_status":"paid","payment_intent":{"status":"requires_payment_method"}}`,
	} {
		var p paymentPage
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatal(err)
		}
		if publicIntentIdle(&p) {
			t.Fatal("inconsistent or processing payment classified idle")
		}
	}
}

func TestExplicitDeclineRequiresIdleUnpaidIntent(t *testing.T) {
	for _, tc := range []struct {
		status, payment, failure, funds string
		want                            bool
	}{
		{"requires_payment_method", "unpaid", `{"code":"card_declined","decline_code":"generic_decline"}`, "", true},
		{"canceled", "unpaid", `{"code":"card_declined"}`, "", true},
		{"requires_payment_method", "unpaid", `null`, "", false},
		{"requires_payment_method", "unpaid", `{"code":"processing_error"}`, "", false},
		{"processing", "unpaid", `{"code":"card_declined"}`, "", false},
		{"requires_action", "unpaid", `{"code":"card_declined"}`, "", false},
		{"requires_capture", "unpaid", `{"code":"card_declined"}`, "", false},
		{"requires_payment_method", "paid", `{"code":"card_declined"}`, "", false},
		{"requires_payment_method", "unpaid", `{"code":"card_declined"}`, `,"amount_received":1`, false},
		{"requires_payment_method", "unpaid", `{"code":"card_declined"}`, `,"amount_capturable":1`, false},
	} {
		var p paymentPage
		raw := fmt.Sprintf(`{"payment_status":%q,"payment_intent":{"status":%q,"last_payment_error":%s%s}}`, tc.payment, tc.status, tc.failure, tc.funds)
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatal(err)
		}
		if got := publicIntentDeclined(&p); got != tc.want {
			t.Fatalf("%s: declined=%t", raw, got)
		}
	}
}
