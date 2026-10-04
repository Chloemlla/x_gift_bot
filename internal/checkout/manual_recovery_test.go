package checkout

import (
	"context"
	"encoding/json"
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
func TestManualRetryUsesSameSessionAndSingleNewAttempt(t *testing.T) {
	for _, outcome := range []string{"succeeded", "declined", "requires_action"} {
		t.Run(outcome, func(t *testing.T) {
			v, r, plan, proof := manualFixture(t)
			originalKey := r.ConfirmKey
			confirms, methods := 0, 0
			s := &stripeClient{vault: v, key: "pk_live_Test", http: &http.Client{Transport: stripeRoundTrip(func(req *http.Request) (*http.Response, error) {
				status := 200
				body := "{}"
				switch {
				case strings.HasSuffix(req.URL.Path, "/poll"):
					payment, state := "requires_payment_method", "active"
					if confirms > 0 && outcome != "declined" {
						payment = outcome
						if outcome == "succeeded" {
							state = "succeeded"
						}
					}
					body = `{"session_id":"cs_live_Test","livemode":true,"is_sandbox_merchant":false,"mode":"payment","success_url":"https://x.com/recipient/gift-premium/success","state":"` + state + `","payment_object_status":"` + payment + `"}`
				case strings.HasSuffix(req.URL.Path, "/init"):
					body = string(proof.Page)
				case strings.HasSuffix(req.URL.Path, "/payment_intents/pi_Test"):
					b, _ := json.Marshal(proof.Intent)
					body = string(b)
				case strings.HasSuffix(req.URL.Path, "/payment_methods"):
					methods++
					body = `{"id":"pm_New","type":"card","livemode":true}`
				case strings.HasSuffix(req.URL.Path, "/confirm"):
					confirms++
					if req.Header.Get("Idempotency-Key") == originalKey {
						t.Fatal("reused original rejected attempt key")
					}
					if outcome == "declined" {
						status = 402
						body = `{"error":{"type":"card_error","code":"card_declined","advice_code":"do_not_try_again","message":"declined"}}`
					}
				default:
					t.Fatalf("unexpected request %s", req.URL.Path)
				}
				return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}}
			result, err := manualRecoverDeclined(context.Background(), v, r, s, plan, func() error { return nil })
			if confirms != 1 || methods != 1 || result.Status != outcome || result.SessionID != r.SessionID || result.RecoveryAttempts != 1 {
				t.Fatalf("bad outcome status=%s confirms=%d methods=%d err=%v", result.Status, confirms, methods, err)
			}
			if err = verifySubmission(v, result, plan); err != nil {
				t.Fatal("recovery evidence cannot be reconciled", err)
			}
			if _, err = v.Get("manual-previous:cs_live_Test:1"); err != nil {
				t.Fatal("missing original attempt archive")
			}
			if outcome == "declined" {
				paused, e := PaymentPaused(v)
				if e != nil || !paused {
					t.Fatal("hard decline did not pause")
				}
				if ResetManualPaymentPause(v) == nil {
					t.Fatal("hard no-retry pause was reset")
				}
			}
			// A duplicate reservation for this attempt cannot submit a second time.
			if err = reservePaymentSlot(context.Background(), v, result); err == nil {
				t.Fatal("duplicate attempt accepted")
			}
		})
	}
}
func TestManualRetryProofFailsClosed(t *testing.T) {
	_, r, plan, proof := manualFixture(t)
	if err := proof.guard(r, plan); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"received", "capturable", "missing_amount", "processing", "wrong_intent", "wrong_recipient", "wrong_price"} {
		t.Run(kind, func(t *testing.T) {
			b, _ := json.Marshal(proof)
			var p manualProof
			json.Unmarshal(b, &p)
			switch kind {
			case "received":
				v := 1
				p.Intent.Received = &v
			case "capturable":
				v := 1
				p.Intent.Capturable = &v
			case "missing_amount":
				p.Intent.Received = nil
			case "processing":
				p.Intent.Status = "processing"
			case "wrong_intent":
				p.Intent.ID = "pi_Other"
			case "wrong_recipient":
				p.Page = []byte(strings.ReplaceAll(string(p.Page), "recipient", "other"))
			case "wrong_price":
				p.Intent.Amount = 1
			}
			if p.guard(r, plan) == nil {
				t.Fatal("unsafe evidence accepted")
			}
		})
	}
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
