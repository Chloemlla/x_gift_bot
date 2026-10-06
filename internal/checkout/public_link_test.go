package checkout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
	"xgift/internal/vault"
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
		{"other browser", "other", func(r *Record) {}, false, false},
		{"stored card", "owner", func(r *Record) { r.CardFingerprint = "private-card" }, false, false},
		{"payment method", "owner", func(r *Record) { r.PaymentMethod = "pm_private" }, false, false},
		{"submitted", "owner", func(r *Record) { r.SubmittedAt = 123 }, false, false},
		{"ambiguous creation", "owner", func(r *Record) { r.Status = "creating" }, false, false},
		{"different plan", "owner", func(r *Record) { r.Months = 3 }, false, false},
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
			got, err := publicLinkExisting(v, "recipient", "1234", tc.owner, plan)
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

func TestPublicLinkExplicitRetryAfterUnpublishedTimeout(t *testing.T) {
	for _, alwaysFail := range []bool{false, true} {
		v := controlFixture(t)
		calls := 0
		transport := mockXTransport(func(req *http.Request) (*http.Response, error) {
			if req.URL.Host != "x.com" {
				t.Fatal("public generation contacted a payment endpoint")
			}
			body := ""
			switch {
			case strings.HasSuffix(req.URL.Path, "/PremiumGiftingQuery"):
				body = `{"data":{"user":{"result":{"rest_id":"1234","premium_gifting_eligible":true,"core":{"screen_name":"recipient"}}}}}`
			case strings.HasSuffix(req.URL.Path, "/useSubscriptionProductDetailsByRestIdQuery"):
				body = `{"data":{"web_subscription_product_details_by_rest_id":{"rest_id":"prod_TEST6MO","prices":[{"amount_local_micro":600000000,"currency_code":"usd","price_type":"OneTime"}]}}}`
			case strings.HasSuffix(req.URL.Path, "/useOneTimePurchaseGiftMutation"):
				calls++
				if calls == 1 || alwaysFail {
					return nil, context.DeadlineExceeded
				}
				body = fmt.Sprintf(`{"data":{"onetimepurchase_gift":{"session_id":"cs_live_TestPublic%d","session_url":"https://checkout.stripe.com/c/pay/cs_live_TestPublic%d","session_status":"Unpaid"}}}`, calls, calls)
			default:
				t.Fatal("unexpected X operation")
			}
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		client := &http.Client{Transport: transport}
		x := &xClient{vault: v, headers: make(http.Header), http: client, regionalHTTP: client}
		plan := Plan{Months: 6, Minor: 60000, Currency: "usd", ProductID: "prod_TEST6MO"}
		x.readCheckout = func(_ context.Context, r *Record) (*paymentPage, error) { return publicPageFixture(r, plan), nil }
		owner := strings.Repeat("a", 64)
		r, e := publicLinkForClient(context.Background(), v, "recipient", owner, plan, x)
		if r != nil || !errors.Is(e, ErrPublicLinkPending) || calls != 1 {
			t.Fatal("first timeout was not safely reserved")
		}
		if alwaysFail {
			for i := 0; i < 2; i++ {
				resetCreationClock(t, v)
				_, e = publicLinkForClient(context.Background(), v, "recipient", owner, plan, x)
				if !errors.Is(e, ErrPublicLinkPending) {
					t.Fatal(e)
				}
			}
			_, e = publicLinkForClient(context.Background(), v, "recipient", owner, plan, x)
			if !errors.Is(e, ErrPublicLinkRetryLimit) || calls != 3 {
				t.Fatal("unbounded public creation retry")
			}
		} else {
			resetCreationClock(t, v)
			r, e = publicLinkForClient(context.Background(), v, "recipient", owner, plan, x)
			if e != nil || r == nil || r.Status != "created" || r.CreationAttempts != 2 || calls != 2 {
				t.Fatalf("retry did not recover: %v", e)
			}
			original := r.URL
			resetCreationClock(t, v)
			r, e = publicLinkForClient(context.Background(), v, "recipient", owner, plan, x)
			if e != nil || r.URL == original || calls != 3 {
				t.Fatal("explicit generation reused a cached checkout")
			}
			_, e = publicLinkForClient(context.Background(), v, "recipient", strings.Repeat("b", 64), plan, x)
			if !errors.Is(e, ErrPublicLinkOtherBrowser) || calls != 3 {
				t.Fatal("another browser accessed the order")
			}
			// A closed session is not evidence of non-payment. Never replace it
			// without the visitor's explicit confirmation, nor return its old URL.
			oldID := r.SessionID
			x.readCheckout = func(_ context.Context, record *Record) (*paymentPage, error) {
				if record.SessionID == oldID {
					return nil, &stripeError{Code: "checkout_not_active_session"}
				}
				return publicPageFixture(record, plan), nil
			}
			resetCreationClock(t, v)
			got, err := publicLinkForClient(context.Background(), v, "recipient", owner, plan, x)
			if got != nil || !errors.Is(err, ErrVerifyUnpaid) || calls != 3 {
				t.Fatal("closed session was reused or replaced without confirmation", err)
			}
			got, err = publicLinkForClient(context.Background(), v, "recipient", owner, plan, x, true)
			if err != nil || got == nil || got.SessionID == oldID || calls != 4 {
				t.Fatal("confirmed closed session did not get a new verified checkout", err)
			}
		}
	}
}

func resetCreationClock(t *testing.T, v *vault.Vault) {
	t.Helper()
	b, _ := json.Marshal(time.Now().Add(-time.Minute).UnixMilli())
	if err := v.Put("checkout-creation:last", b); err != nil {
		t.Fatal(err)
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
			if (tc.name == "expired" || tc.name == "inactive") && !errors.Is(err, ErrVerifyUnpaid) {
				t.Fatal(err)
			}
		})
	}
}
