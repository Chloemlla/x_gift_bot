package checkout

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestManualLinkCannotReplaceDifferentOrPaidOrder(t *testing.T) {
	v := controlFixture(t)
	if err := v.Put("catalog", []byte(`{"merchant":"acct_TESTMERCHANT","currency":"usd","plans":[{"months":3,"amount":30000,"product":"prod_TEST3MO"},{"months":6,"amount":60000,"product":"prod_TEST6MO"}]}`)); err != nil {
		t.Fatal(err)
	}
	base := Record{Username: "recipient", RecipientID: "1234", Months: 6, Amount: 60000, Currency: "USD", ProductID: "prod_TEST6MO", SessionID: "cs_live_Original123", URL: "https://checkout.stripe.com/c/pay/cs_live_Original123", Status: "succeeded", Created: 123}
	for _, tc := range []struct {
		name         string
		change       func(*Record)
		wantConflict bool
	}{
		{"paid order", func(r *Record) {}, false},
		{"different duration", func(r *Record) { r.Months = 3 }, true},
		{"different user", func(r *Record) { r.Username = "other" }, true},
		{"different recipient", func(r *Record) { r.RecipientID = "9999" }, true},
		{"different price", func(r *Record) { r.Amount = 1 }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			tc.change(&r)
			b, _ := json.Marshal(r)
			if err := v.Put("checkout:1234", b); err != nil {
				t.Fatal(err)
			}
			got, err := manualLinkForRecipient(context.Background(), v, "recipient", "1234", 0, 6, true)
			if tc.wantConflict {
				if !errors.Is(err, ErrManualLinkConflict) {
					t.Fatalf("wrong result: %v", err)
				}
			} else if err != nil || got == nil || got.Status != "succeeded" {
				t.Fatalf("paid order not preserved: %v", err)
			}
			after, _ := v.Get("checkout:1234")
			if string(after) != string(b) {
				t.Fatal("existing payment changed")
			}
		})
	}
}

func TestManualLinkUnpaidCheckboxCannotOverrideUnknownPayment(t *testing.T) {
	v := controlFixture(t)
	v.Put("catalog", []byte(`{"merchant":"acct_TESTMERCHANT","currency":"usd","plans":[{"months":6,"amount":60000,"product":"prod_TEST6MO"}]}`))
	r := Record{Username: "recipient", RecipientID: "1234", Months: 6, Amount: 60000, Currency: "USD", ProductID: "prod_TEST6MO", SessionID: "cs_live_Original123", URL: "https://checkout.stripe.com/c/pay/cs_live_Original123", Status: "requires_action", Created: 123, SubmittedAt: 124}
	b, _ := json.Marshal(r)
	v.Put("checkout:1234", b)
	// Deliberately no valid confirmation proof or Stripe credentials. Even an
	// operator checkbox must never erase the pending order to create a new one.
	if _, err := manualLinkForRecipient(context.Background(), v, "recipient", "1234", 0, 6, true); err == nil {
		t.Fatal("unverified payment accepted")
	}
	after, _ := v.Get("checkout:1234")
	if string(after) != string(b) {
		t.Fatal("unverified payment was replaced")
	}
}
