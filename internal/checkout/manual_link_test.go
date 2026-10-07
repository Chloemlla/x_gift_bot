package checkout

import (
	"context"
	"encoding/json"
	"testing"
)

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
