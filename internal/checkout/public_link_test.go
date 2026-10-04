package checkout

import (
	"encoding/json"
	"errors"
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
