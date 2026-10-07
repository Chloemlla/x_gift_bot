package checkout

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"xgift/internal/vault"
)

func manualFixture(t *testing.T) (*vault.Vault, *Record, Plan, manualProof) {
	t.Helper()
	v := controlFixture(t)
	if err := v.Put("card", []byte(`{"number":"4242424242424242","exp_month":"12","exp_year":"2099","cvc":"123","billing_name":"Test","email":"test@example.invalid","billing_country":"US"}`)); err != nil {
		t.Fatal(err)
	}
	r := &Record{Username: "recipient", RecipientID: "1234", Months: 3, Amount: 30000, Currency: "USD", ProductID: "prod_Test", SessionID: "cs_live_Test", URL: "https://checkout.stripe.com/a/pay/cs_live_Test", Status: "declined", Created: 123, SubmittedAt: 124, PaymentMethod: "pm_Old", LastError: &stripeError{HTTP: 402, Type: "card_error", Code: "card_declined"}}
	r.ConfirmKey = idempotency(r, "confirm")
	r.ConfirmParameters = url.Values{"payment_method": {"pm_Old"}, "expected_amount": {"30000"}, "expected_payment_method_type": {"card"}, "init_checksum": {"original"}, "return_url": {"https://x.com/recipient/gift-premium/success"}}.Encode()
	if err := save(v, r); err != nil {
		t.Fatal(err)
	}
	plan := Plan{Months: 3, Minor: 30000, Currency: "usd", Merchant: "acct_Test", ProductID: "prod_Test"}
	raw := []byte(`{"session_id":"cs_live_Test","currency":"usd","mode":"payment","livemode":true,"status":"open","payment_status":"unpaid","init_checksum":"new","success_url":"https://x.com/recipient/gift-premium/success","cancel_url":"https://x.com/recipient/gift-premium","account_settings":{"account_id":"acct_Test"},"total_summary":{"due":30000,"subtotal":30000,"total":30000},"line_item_group":{"currency":"usd","due":30000,"subtotal":30000,"total":30000,"line_items":[{"name":"Premium Gift - 3 months","quantity":1,"subtotal":30000,"total":30000,"price":{"currency":"usd","type":"one_time","unit_amount":30000,"product":{"id":"prod_Test","name":"Premium Gift - 3 months","livemode":true}}}]},"payment_intent":{"id":"pi_Test","client_secret":"pi_Test_secret_Synthetic","status":"requires_payment_method","currency":"usd","amount":30000,"amount_received":0}}`)
	zero := 0
	proof := manualProof{Page: raw, Intent: &stripeIntentEvidence{ID: "pi_Test", Status: "requires_payment_method", Currency: "usd", Live: true, Amount: 30000, Received: &zero, Capturable: &zero}}
	return v, r, plan, proof
}
func TestManualPreflightFailureNeverConfirms(t *testing.T) {
	v, r, plan, proof := manualFixture(t)
	proof.Page = []byte(strings.ReplaceAll(string(proof.Page), `"unpaid"`, `"paid"`))
	confirms := 0
	s := &stripeClient{vault: v, key: "pk_live_Test", http: &http.Client{Transport: stripeRoundTrip(func(req *http.Request) (*http.Response, error) {
		body := string(proof.Page)
		if strings.HasSuffix(req.URL.Path, "/poll") {
			body = `{"session_id":"cs_live_Test","livemode":true,"is_sandbox_merchant":false,"mode":"payment","success_url":"https://x.com/recipient/gift-premium/success","state":"active","payment_object_status":"requires_payment_method"}`
		}
		if strings.HasSuffix(req.URL.Path, "/confirm") {
			confirms++
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
	_, err := manualRecoverDeclined(context.Background(), v, r, s, plan, func() error { return nil })
	if err == nil || confirms != 0 {
		t.Fatal("failed preflight submitted payment")
	}
}
