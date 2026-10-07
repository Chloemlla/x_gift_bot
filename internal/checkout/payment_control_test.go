package checkout

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"xgift/internal/vault"
)

func controlFixture(t *testing.T) *vault.Vault {
	t.Helper()
	dir := t.TempDir()
	password := filepath.Join(dir, "password")
	if err := os.WriteFile(password, []byte(strings.Repeat("p", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	v, err := vault.Open(filepath.Join(dir, "vault.db"), password, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { v.Close() })
	return v
}

type stripeRoundTrip func(*http.Request) (*http.Response, error)

func (f stripeRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDeclinePersistsDetailsAndStopsPolling(t *testing.T) {
	v := controlFixture(t)
	r := Record{Username: "recipient", RecipientID: "1234", Months: 3, Amount: 30000, Currency: "USD", ProductID: "prod_TEST3MO", SessionID: "cs_live_TestDecline", URL: "https://checkout.stripe.com/a/pay/cs_live_TestDecline", Created: 123, SubmittedAt: 124, Status: "submitting", PaymentMethod: "pm_Test"}
	r.ConfirmKey = idempotency(&r, "confirm")
	r.ConfirmParameters = url.Values{"payment_method": {"pm_Test"}, "expected_amount": {"30000"}, "expected_payment_method_type": {"card"}, "init_checksum": {"test"}, "return_url": {"https://x.com/recipient/gift-premium/success"}}.Encode()
	if err := savePaymentControl(v, paymentControl{LastSession: r.SessionID}); err != nil {
		t.Fatal(err)
	}
	confirm, poll := 0, 0
	s := &stripeClient{vault: v, key: "pk_live_Test", http: &http.Client{Transport: stripeRoundTrip(func(req *http.Request) (*http.Response, error) {
		status, body := 200, `{"session_id":"cs_live_TestDecline","livemode":true,"is_sandbox_merchant":false,"mode":"payment","success_url":"https://x.com/recipient/gift-premium/success","state":"active","payment_object_status":"requires_payment_method"}`
		if strings.HasSuffix(req.URL.Path, "/confirm") {
			confirm++
			status = 402
			body = `{"error":{"type":"card_error","code":"card_declined","decline_code":"insufficient_funds","advice_code":"try_again_later","network_decline_code":"51","message":"Your card was declined."}}`
		} else {
			poll++
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Request-Id": {"req_Test"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
	result, err := confirmAndObserve(context.Background(), v, &r, s, Plan{Months: 3, Minor: 30000, Currency: "usd", ProductID: "prod_TEST3MO"})
	if err == nil || result.Status != "declined" || confirm != 1 || poll != 1 {
		t.Fatalf("result=%+v err=%v confirms=%d polls=%d", result, err, confirm, poll)
	}
	b, err := v.Get("checkout:1234")
	if err != nil {
		t.Fatal(err)
	}
	var saved Record
	if err = json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.LastError.DeclineCode != "insufficient_funds" || saved.LastError.AdviceCode != "try_again_later" || saved.LastError.NetworkDeclineCode != "51" {
		t.Fatalf("missing decline details: %+v", saved.LastError)
	}
	if _, err = v.Get("stripe-error:" + r.SessionID); err != nil {
		t.Fatal("per-order error evidence missing", err)
	}
	if err = v.Put("catalog", []byte(`{"merchant":"acct_Test","currency":"usd","plans":[{"months":3,"amount":30000,"product":"prod_TEST3MO"}]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = ResumeForRecipient(context.Background(), v, "recipient", "1234", 0, 3); !errors.Is(err, ErrPaymentDeclined) {
		t.Fatalf("declined order could be resumed: %v", err)
	}
}

func TestSpacingUsesPersistedTimeAndCancellationDoesNotReserve(t *testing.T) {
	v := controlFixture(t)
	now := time.Now()
	if err := savePaymentControl(v, paymentControl{LastSubmittedAt: now.UnixNano(), LastSession: "cs_live_Previous"}); err != nil {
		t.Fatal(err)
	}
	state, err := readPaymentControl(v)
	if err != nil {
		t.Fatal(err)
	}
	if got := paymentWait(state, now.Add(10*time.Second)); got != 20*time.Second {
		t.Fatalf("spacing after reload=%s", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = reservePaymentSlot(ctx, v, &Record{SessionID: "cs_live_Next"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled wait: %v", err)
	}
	after, _ := readPaymentControl(v)
	if after.LastSession != state.LastSession || after.LastSubmittedAt != state.LastSubmittedAt {
		t.Fatal("canceled request changed reserved slot")
	}
}

func TestDoNotRetryAdvicePausesOnFirstDecline(t *testing.T) {
	v := controlFixture(t)
	r := &Record{SessionID: "cs_live_NoRetry", Status: "declined", LastError: &stripeError{HTTP: 402, Type: "card_error", AdviceCode: "do_not_try_again"}}
	if err := savePaymentControl(v, paymentControl{LastSession: r.SessionID}); err != nil {
		t.Fatal(err)
	}
	if err := paymentOutcome(v, r); err != nil {
		t.Fatal(err)
	}
	if paused, _ := PaymentPaused(v); !paused {
		t.Fatal("do_not_try_again did not pause")
	}
}
