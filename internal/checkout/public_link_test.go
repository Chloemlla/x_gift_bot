package checkout

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
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
				body = `{"data":{"onetimepurchase_gift":{"session_id":"cs_live_TestPublic123","session_url":"https://checkout.stripe.com/c/pay/cs_live_TestPublic123","session_status":"Unpaid"}}}`
			default:
				t.Fatal("unexpected X operation")
			}
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		client := &http.Client{Transport: transport}
		x := &xClient{vault: v, headers: make(http.Header), http: client, regionalHTTP: client}
		plan := Plan{Months: 6, Minor: 60000, Currency: "usd", ProductID: "prod_TEST6MO"}
		owner := strings.Repeat("a", 64)
		r, e := publicLinkForClient(context.Background(), v, "recipient", owner, plan, x)
		if r != nil || !errors.Is(e, ErrPublicLinkPending) || calls != 1 {
			t.Fatal("first timeout was not safely reserved")
		}
		if alwaysFail {
			for i := 0; i < 2; i++ {
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
			r, e = publicLinkForClient(context.Background(), v, "recipient", owner, plan, x)
			if e != nil || r == nil || r.Status != "created" || r.CreationAttempts != 2 || calls != 2 {
				t.Fatalf("retry did not recover: %v", e)
			}
			original := r.URL
			r, e = publicLinkForClient(context.Background(), v, "recipient", owner, plan, x)
			if e != nil || r.URL != original || calls != 2 {
				t.Fatal("published link was replaced")
			}
			_, e = publicLinkForClient(context.Background(), v, "recipient", strings.Repeat("b", 64), plan, x)
			if !errors.Is(e, ErrPublicLinkOtherBrowser) || calls != 2 {
				t.Fatal("another browser accessed the order")
			}
		}
	}
}
