package checkout

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestActiveCheckoutWindowAndEarlyCompletion(t *testing.T) {
	for _, state := range []string{"open", "paid", "inactive", "processing", "wrong_session", "expired"} {
		t.Run(state, func(t *testing.T) {
			v := controlFixture(t)
			now := time.Now()
			p := Plan{Months: 6, Minor: 60000, Currency: "usd", ProductID: "prod_TEST6MO", Merchant: "acct_Test"}
			r := Record{Username: "recipient", RecipientID: "1234", Months: 6, Amount: 60000, Currency: "USD", ProductID: p.ProductID, Created: now.Add(-time.Minute).Unix(), Status: "created", SessionID: "cs_live_Active", URL: "https://checkout.stripe.com/c/pay/cs_live_Active"}
			if state == "expired" {
				r.Created = now.Add(-publicLinkTTL).Unix()
			}
			a := activeCheckout{Order: r, Plan: p, ExpiresAt: time.Unix(r.Created, 0).Add(publicLinkTTL).UnixMilli()}
			if err := saveActiveCheckout(v, a); err != nil {
				t.Fatal(err)
			}
			reads := 0
			x := &xClient{vault: v, readCheckout: func(_ context.Context, rec *Record) (*paymentPage, error) {
				reads++
				page := publicPageFixture(rec, p)
				switch state {
				case "paid":
					page.Status = "complete"
					page.PaymentStatus = "paid"
				case "inactive":
					return nil, &stripeError{Code: "checkout_not_active_session"}
				case "processing":
					page.IntentNull = false
				case "wrong_session":
					page.SessionID = "cs_live_Wrong"
				}
				return page, nil
			}}
			err := x.checkCreation(context.Background(), now)
			if state == "paid" || state == "expired" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var wait *CheckoutWaitError
				if !errors.As(err, &wait) || wait.Wait < time.Minute {
					t.Fatalf("unprotected active order: %v", err)
				}
			}
			if state == "expired" && reads != 1 {
				t.Fatal("expired window skipped final payment verification")
			}
			stored, e := readActiveCheckout(v)
			if e != nil {
				t.Fatal(e)
			}
			if stored.ExpiresAt != a.ExpiresAt {
				t.Fatal("window was extended")
			}
			if stored.Released != (state == "paid" || state == "expired") {
				t.Fatal("early release without paid evidence")
			}
			wait, e := CheckoutCreationWait(v, time.Now())
			if e != nil {
				t.Fatal(e)
			}
			if state == "paid" || state == "expired" {
				if wait != 0 {
					t.Fatal("released wait remains")
				}
			} else if wait < time.Minute {
				t.Fatal("persisted wait lost")
			}
			// A newly constructed client sees the durable reservation after a restart.
			if state == "open" {
				restarted := &xClient{vault: v, readCheckout: x.readCheckout}
				if err := restarted.checkCreation(context.Background(), time.Now()); !errors.Is(err, ErrCheckoutRateLimited) {
					t.Fatal("restart bypassed active reservation", err)
				}
			}
		})
	}
}

func TestActiveCheckoutBootstrapsMostRecentPublicLink(t *testing.T) {
	v := controlFixture(t)
	now := time.Now()
	for i, user := range []string{"older", "latest"} {
		r := Record{Username: user, RecipientID: user, Months: 6, Amount: 60000, Currency: "USD", ProductID: "prod_Test", Created: now.Add(-time.Duration(2-i) * time.Minute).Unix(), Status: "created", SessionID: "cs_live_" + user, URL: "https://checkout.stripe.com/c/pay/cs_live_" + user}
		b, _ := json.Marshal(publicLinkRecord{Order: r})
		if err := v.Put("public-checkout:"+user, b); err != nil {
			t.Fatal(err)
		}
	}
	a, err := bootstrapActiveCheckout(v, now)
	if err != nil || a.Order.Username != "latest" {
		t.Fatalf("failed to preserve latest published link: %v", err)
	}
	originalExpiry := a.ExpiresAt
	a.Released = true
	if err := saveActiveCheckout(v, a); err != nil {
		t.Fatal(err)
	}
	again, err := bootstrapActiveCheckout(v, now.Add(time.Minute))
	if err != nil || !again.Released || again.ExpiresAt != originalExpiry {
		t.Fatal("migration reran or extended expiry", err)
	}
}

func TestPaidReleaseStillHonorsCreationMinimum(t *testing.T) {
	v := controlFixture(t)
	now := time.Now()
	p := Plan{Months: 6, Minor: 60000, Currency: "usd", ProductID: "prod_Test"}
	r := Record{Username: "recipient", SessionID: "cs_live_Test", Created: now.Unix(), Status: "created"}
	if err := saveActiveCheckout(v, activeCheckout{Order: r, Plan: p, ExpiresAt: time.Unix(r.Created, 0).Add(publicLinkTTL).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if err := reserveCheckoutCreation(v, now); err != nil {
		t.Fatal(err)
	}
	x := &xClient{vault: v, readCheckout: func(_ context.Context, r *Record) (*paymentPage, error) {
		p := publicPageFixture(r, p)
		p.Status = "complete"
		p.PaymentStatus = "paid"
		return p, nil
	}}
	var wait *CheckoutWaitError
	if err := x.checkCreation(context.Background(), now); !errors.As(err, &wait) || wait.Wait > 15*time.Second {
		t.Fatalf("minimum interval bypassed: %v", err)
	}
}

func TestActiveCheckoutPollReleasesCompletedInactiveSession(t *testing.T) {
	v := controlFixture(t)
	now := time.Now()
	plan := Plan{Months: 6, Minor: 60000, Currency: "usd", ProductID: "prod_Test"}
	r := Record{Username: "recipient", SessionID: "cs_live_Test", Created: now.Add(-time.Minute).Unix(), Status: "created"}
	if err := saveActiveCheckout(v, activeCheckout{Order: r, Plan: plan, ExpiresAt: time.Unix(r.Created, 0).Add(publicLinkTTL).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	polls := 0
	x := &xClient{vault: v, readCheckout: func(context.Context, *Record) (*paymentPage, error) {
		return nil, &stripeError{Code: "checkout_not_active_session"}
	}, readCheckoutPaid: func(context.Context, *Record, Plan) (bool, error) { polls++; return true, nil }}
	if err := x.checkCreation(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	a, err := readActiveCheckout(v)
	if err != nil || !a.Released || polls != 1 {
		t.Fatalf("paid poll did not release window: %v polls=%d", err, polls)
	}
}

func TestLegacyPaymentWindowUsesThreeMinuteDeadlineAfterUpgrade(t *testing.T) {
	v := controlFixture(t)
	now := time.Now().Truncate(time.Second)
	r := Record{SessionID: "cs_live_LegacyWindow", Created: now.Add(-time.Minute).Unix()}
	a := activeCheckout{Order: r, ExpiresAt: time.Unix(r.Created, 0).Add(15 * time.Minute).UnixMilli()}
	if err := saveActiveCheckout(v, a); err != nil {
		t.Fatal(err)
	}
	wait, err := CheckoutCreationWait(v, now)
	if err != nil || wait != 2*time.Minute {
		t.Fatal("legacy reservation not shortened", wait, err)
	}
	wait, err = CheckoutCreationWait(v, now.Add(2*time.Minute))
	if err != nil || wait != 0 {
		t.Fatal("three-minute boundary still blocked", wait, err)
	}
	a.ExpiresAt++
	saveActiveCheckout(v, a)
	if _, err = CheckoutCreationWait(v, now); err == nil {
		t.Fatal("accepted malformed reservation")
	}
}

func TestExpiredWindowNeverInterruptsPaymentOrAssumesNetworkFailureIsUnpaid(t *testing.T) {
	for _, state := range []string{"processing", "requires_action", "network_error", "wrong_session"} {
		t.Run(state, func(t *testing.T) {
			v := controlFixture(t)
			now := time.Now()
			p := Plan{Months: 6, Minor: 60000, Currency: "usd", ProductID: "prod_TEST6MO", Merchant: "acct_Test"}
			r := Record{Username: "recipient", RecipientID: "1234", Months: 6, Amount: 60000, Currency: "USD", ProductID: p.ProductID, Created: now.Add(-4 * time.Minute).Unix(), Status: "created", SessionID: "cs_live_Active", URL: "https://checkout.stripe.com/c/pay/cs_live_Active"}
			a := activeCheckout{Order: r, Plan: p, ExpiresAt: time.Unix(r.Created, 0).Add(publicLinkTTL).UnixMilli()}
			if err := saveActiveCheckout(v, a); err != nil {
				t.Fatal(err)
			}
			x := &xClient{vault: v, readCheckout: func(_ context.Context, rec *Record) (*paymentPage, error) {
				if state == "network_error" {
					return nil, errors.New("temporary network failure")
				}
				page := publicPageFixture(rec, p)
				if state == "wrong_session" {
					page.SessionID = "cs_live_Wrong"
				} else {
					raw, _ := json.Marshal(page)
					var fields map[string]any
					json.Unmarshal(raw, &fields)
					fields["payment_intent"] = map[string]any{"id": "pi_Synthetic", "status": state, "amount": 60000, "currency": "usd", "amount_received": 0}
					raw, _ = json.Marshal(fields)
					json.Unmarshal(raw, page)
				}
				return page, nil
			}}
			var wait *CheckoutWaitError
			if err := x.checkCreation(context.Background(), now); !errors.As(err, &wait) {
				t.Fatal("expired in-flight payment was replaced", err)
			}
			stored, err := readActiveCheckout(v)
			if err != nil || stored.Released || stored.ExpiresAt != a.ExpiresAt {
				t.Fatal("lost payment protection or extended idle TTL", err)
			}
		})
	}
}
