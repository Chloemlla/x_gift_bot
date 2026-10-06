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
			resetCreationClock(t, v)
			_, e = publicLinkForClient(context.Background(), v, "recipient", owner, plan, x)
			if !errors.Is(e, ErrPublicLinkPending) || calls != 4 {
				t.Fatal("manual retry was permanently blocked", e)
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
			if e != nil || r.URL != original || calls != 2 {
				t.Fatal("fresh verified checkout was not reused")
			}
			other, e := publicLinkForClient(context.Background(), v, "recipient", strings.Repeat("b", 64), plan, x)
			if e != nil || other.URL != original || calls != 2 {
				t.Fatal("another browser could not retrieve verified public order")
			}
			// An explicit manual-link request may replace an inactive session;
			// this never declares the old payment failed or submits a payment.
			oldID := r.SessionID
			x.readCheckout = func(_ context.Context, record *Record) (*paymentPage, error) {
				if record.SessionID == oldID {
					return nil, &stripeError{Code: "checkout_not_active_session"}
				}
				return publicPageFixture(record, plan), nil
			}
			resetCreationClock(t, v)
			got, err := publicLinkForClient(context.Background(), v, "recipient", owner, plan, x)
			if err != nil || got == nil || got.SessionID == oldID || calls != 3 {
				t.Fatal("inactive link could not be replaced directly", err)
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

func TestPublicLinkTTLAndPlanReplacement(t *testing.T) {
	plan := Plan{Months: 6, Minor: 60000, Currency: "usd", ProductID: "prod_TEST6MO"}
	for _, tc := range []struct {
		name       string
		age        time.Duration
		changePlan bool
		state      string
		creates    int
		wantErr    bool
	}{
		{"cross browser cache", time.Minute, false, "open", 0, false},
		{"stored unverified session", time.Minute, false, "wrong_session", 0, true},
		{"expired TTL", 15 * time.Minute, false, "open", 1, false},
		{"unknown creation time", 0, false, "open", 1, false},
		{"changed plan immediately replaces own window", time.Minute, true, "open", 1, false},
		{"changed plan after failed manual payment", time.Minute, true, "requires_payment_method", 1, false},
		{"other account window protected", time.Minute, true, "other_active", 0, true},
		{"changed plan after expiry", 16 * time.Minute, true, "open", 1, false},
		{"processing within TTL", time.Minute, false, "processing", 0, true},
		{"processing beyond TTL", 16 * time.Minute, false, "processing", 0, true},
		{"expired with processing intent", 16 * time.Minute, false, "expired_processing", 0, true},
		{"inactive beyond TTL", 16 * time.Minute, false, "inactive", 1, false},
		{"inactive within TTL", time.Minute, false, "inactive", 1, false},
		{"paid beyond TTL", 16 * time.Minute, true, "paid", 0, false},
		{"upstream repeats expired session", 16 * time.Minute, false, "replay", 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := controlFixture(t)
			old := Record{Username: "recipient", RecipientID: "1234", Months: plan.Months, Amount: plan.Minor, Currency: "USD", ProductID: plan.ProductID, Status: "created", SessionID: "cs_live_Original", URL: "https://checkout.stripe.com/c/pay/cs_live_Original", Created: time.Now().Add(-tc.age).Unix()}
			if tc.age == 0 {
				old.Created = 0
			}
			b, _ := json.Marshal(publicLinkRecord{Owner: "old-browser", Order: old})
			if err := v.Put("public-checkout:1234", b); err != nil {
				t.Fatal(err)
			}
			requested := plan
			if tc.changePlan {
				requested.Months = 3
				requested.Minor = 30000
				requested.ProductID = "prod_TEST3MO"
			}
			creates, reads := 0, 0
			transport := mockXTransport(func(req *http.Request) (*http.Response, error) {
				body := ""
				switch {
				case strings.HasSuffix(req.URL.Path, "/PremiumGiftingQuery"):
					body = `{"data":{"user":{"result":{"rest_id":"1234","premium_gifting_eligible":true,"core":{"screen_name":"recipient"}}}}}`
				case strings.HasSuffix(req.URL.Path, "/useSubscriptionProductDetailsByRestIdQuery"):
					body = fmt.Sprintf(`{"data":{"web_subscription_product_details_by_rest_id":{"rest_id":%q,"prices":[{"amount_local_micro":%d,"currency_code":"usd","price_type":"OneTime"}]}}}`, requested.ProductID, requested.Minor*10000)
				case strings.HasSuffix(req.URL.Path, "/useOneTimePurchaseGiftMutation"):
					creates++
					id := "cs_live_Replacement"
					if tc.state == "replay" {
						id = old.SessionID
					}
					body = fmt.Sprintf(`{"data":{"onetimepurchase_gift":{"session_id":%q,"session_url":%q,"session_status":"Unpaid"}}}`, id, "https://checkout.stripe.com/c/pay/"+id)
				default:
					t.Fatalf("unexpected request %s", req.URL.Path)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			client := &http.Client{Transport: transport}
			x := &xClient{vault: v, headers: make(http.Header), http: client, regionalHTTP: client}
			x.readCheckout = func(_ context.Context, r *Record) (*paymentPage, error) {
				reads++
				if r.SessionID != old.SessionID {
					return publicPageFixture(r, requested), nil
				}
				p := publicPageFixture(r, plan)
				switch tc.state {
				case "wrong_session":
					p.SessionID = "cs_live_WrongRecipientSession"
				case "inactive":
					return nil, &stripeError{Code: "checkout_not_active_session"}
				case "requires_payment_method":
					p.IntentNull = false
					json.Unmarshal([]byte(fmt.Sprintf(`{"currency":%q,"amount":%d,"status":"requires_payment_method","amount_received":0}`, plan.Currency, plan.Minor)), &p.Intent)
				case "processing", "expired_processing":
					if tc.state == "expired_processing" {
						p.Status = "expired"
					}
					p.IntentNull = false
					if err := json.Unmarshal([]byte(fmt.Sprintf(`{"currency":%q,"amount":%d,"status":"processing"}`, plan.Currency, plan.Minor)), &p.Intent); err != nil {
						t.Fatal(err)
					}
				case "paid":
					p.Status = "complete"
					p.PaymentStatus = "paid"
				}
				return p, nil
			}
			// A cache hit must work even while the global creation clock is busy.
			if tc.creates == 0 {
				clock, _ := json.Marshal(time.Now().UnixMilli())
				v.Put("checkout-creation:last", clock)
			}
			if tc.state == "other_active" {
				other := old
				other.Username, other.RecipientID, other.SessionID = "other", "5678", "cs_live_Other"
				other.URL = "https://checkout.stripe.com/c/pay/cs_live_Other"
				if err := saveActiveCheckout(v, activeCheckout{Order: other, Plan: plan, ExpiresAt: time.Unix(other.Created, 0).Add(publicLinkTTL).UnixMilli()}); err != nil {
					t.Fatal(err)
				}
				resetCreationClock(t, v)
			}
			beforeClock, _ := v.Get("checkout-creation:last")
			got, err := publicLinkForClient(context.Background(), v, "recipient", strings.Repeat("b", 64), requested, x, tc.state == "processing" || tc.state == "expired_processing")
			if (err != nil) != tc.wantErr || creates != tc.creates || reads < 1 {
				t.Fatalf("got error=%v creates=%d reads=%d", err, creates, reads)
			}
			if tc.wantErr {
				if got != nil {
					t.Fatal("unsafe link returned")
				}
				if tc.state == "replay" {
					raw, _ := v.Get("public-checkout:1234")
					var stored publicLinkRecord
					json.Unmarshal(raw, &stored)
					if stored.Order.SessionID != old.SessionID || stored.Order.Created != old.Created {
						t.Fatal("upstream replay became cache")
					}
				}
				return
			}
			if tc.state == "paid" {
				if got.Status != "succeeded" {
					t.Fatal("paid order not preserved")
				}
				return
			}
			if !publicLinkMatches(got, requested) {
				t.Fatal("returned wrong plan")
			}
			if tc.creates == 0 {
				if got.SessionID != old.SessionID || got.Created != old.Created {
					t.Fatal("cache hit changed session or extended TTL")
				}
				after, _ := v.Get("public-checkout:1234")
				if string(after) != string(b) {
					t.Fatal("cache hit rewrote record")
				}
				afterClock, _ := v.Get("checkout-creation:last")
				if string(afterClock) != string(beforeClock) {
					t.Fatal("cache consumed creation rate limit")
				}
			} else if got.SessionID == old.SessionID || !publicLinkFresh(got, time.Now()) {
				t.Fatal("replacement did not start a new TTL")
			}
		})
	}
}

func TestPublicLinkTTLBoundary(t *testing.T) {
	created := time.Unix(1700000000, 0)
	r := Record{Created: created.Unix()}
	for _, tc := range []struct {
		offset time.Duration
		fresh  bool
	}{{-time.Second, false}, {0, true}, {publicLinkTTL - time.Nanosecond, true}, {publicLinkTTL, false}, {publicLinkTTL + time.Minute, false}} {
		if got := publicLinkFresh(&r, created.Add(tc.offset)); got != tc.fresh {
			t.Fatalf("age %v: fresh=%t", tc.offset, got)
		}
	}
}

func TestCachedPublicLinkNeverCreatesAndChecksExpiryAfterVerification(t *testing.T) {
	for _, state := range []string{"fresh", "expires_during_verification", "paid_inactive"} {
		t.Run(state, func(t *testing.T) {
			v := controlFixture(t)
			p := Plan{Months: 6, Minor: 60000, Currency: "usd", ProductID: "prod_Test"}
			r := Record{Username: "recipient", RecipientID: "1234", Months: 6, Amount: 60000, Currency: "USD", ProductID: p.ProductID, Status: "created", SessionID: "cs_live_Test", URL: "https://checkout.stripe.com/c/pay/cs_live_Test", Created: time.Now().Unix()}
			b, _ := json.Marshal(publicLinkRecord{Order: r})
			if err := v.Put("public-checkout:1234", b); err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Transport: mockXTransport(func(req *http.Request) (*http.Response, error) {
				if !strings.HasSuffix(req.URL.Path, "/PremiumGiftingQuery") {
					t.Fatal("cache-only path attempted non-identity operation", req.URL.Path)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":{"user":{"result":{"rest_id":"1234","premium_gifting_eligible":true,"core":{"screen_name":"recipient"}}}}}`))}, nil
			})}
			x := &xClient{vault: v, headers: make(http.Header), http: client, regionalHTTP: client, readCheckout: func(_ context.Context, r *Record) (*paymentPage, error) {
				if state == "paid_inactive" {
					return nil, &stripeError{Code: "checkout_not_active_session"}
				}
				if state == "expires_during_verification" {
					r.Created = time.Now().Add(-publicLinkTTL).Unix()
				}
				return publicPageFixture(r, p), nil
			}, readCheckoutPaid: func(context.Context, *Record, Plan) (bool, error) { return state == "paid_inactive", nil }}
			got, hit, err := cachedPublicLinkForClient(context.Background(), v, "recipient", strings.Repeat("b", 64), p, x)
			if err != nil {
				t.Fatal(err)
			}
			if state == "expires_during_verification" {
				if hit || got != nil {
					t.Fatal("returned checkout past its deadline")
				}
				return
			}
			if !hit || got == nil {
				t.Fatal("valid cached result unavailable")
			}
			if state == "paid_inactive" && got.Status != "succeeded" {
				t.Fatal("paid inactive checkout treated as unpaid")
			}
		})
	}
}
