package checkout

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestAuthenticationChallengeCoolsSubmittedCardWithoutRetry(t *testing.T) {
	v := controlFixture(t)
	if _, err := AddCardRecords(v, []byte("["+testCardOne+","+testCardTwo+"]")); err != nil {
		t.Fatal(err)
	}
	cards, err := readCards(v)
	if err != nil {
		t.Fatal(err)
	}
	fp := cardFingerprint(cards[0])
	r := &Record{Username: "recipient", RecipientID: "1234", Months: 3, Amount: 30000, Currency: "USD", ProductID: "prod_TEST3MO", SessionID: "cs_live_Authentication", URL: "https://checkout.stripe.com/a/pay/cs_live_Authentication", Created: 123, SubmittedAt: 124, Status: "submitting", PaymentMethod: "pm_Test", CardFingerprint: fp}
	r.ConfirmKey = idempotency(r, "confirm")
	r.ConfirmParameters = url.Values{"payment_method": {"pm_Test"}, "expected_amount": {"30000"}, "expected_payment_method_type": {"card"}, "init_checksum": {"test"}, "return_url": {"https://x.com/recipient/gift-premium/success"}}.Encode()
	if err := savePaymentRotation(v, &paymentRotation{CardFingerprint: fp, NodeID: "direct", Used: 1}); err != nil {
		t.Fatal(err)
	}
	if err := savePaymentControl(v, paymentControl{LastSession: r.SessionID}); err != nil {
		t.Fatal(err)
	}
	confirms := 0
	s := &stripeClient{vault: v, key: "pk_live_Test", http: &http.Client{Transport: stripeRoundTrip(func(req *http.Request) (*http.Response, error) {
		body := `{}`
		if strings.HasSuffix(req.URL.Path, "/confirm") {
			confirms++
		} else {
			body = `{"session_id":"cs_live_Authentication","livemode":true,"is_sandbox_merchant":false,"mode":"payment","success_url":"https://x.com/recipient/gift-premium/success","state":"active","payment_object_status":"requires_action"}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
	result, err := confirmAndObserve(context.Background(), v, r, s, Plan{Months: 3, Minor: 30000, Currency: "usd", ProductID: "prod_TEST3MO"})
	if err == nil || result.Status != "requires_action" || confirms != 1 || IsPaymentDeclined(result) {
		t.Fatalf("challenge must stay pending, confirms=%d err=%v", confirms, err)
	}
	blocks, err := readCardBlocks(v)
	if err != nil {
		t.Fatal(err)
	}
	b := blocks[fp]
	if b.Reason != "requires_action" || b.Until < time.Now().Add(29*time.Minute).Unix() {
		t.Fatalf("missing cooldown: %+v", b)
	}
	if blocked, err := paymentCardBlocked(v, cardFingerprint(cards[1])); err != nil || blocked {
		t.Fatal("unrelated card blocked", err)
	}
	rotation, err := readPaymentRotation(v)
	if err != nil || rotation.Used != PaymentBatchSize {
		t.Fatal("challenged batch was not expired", err)
	}
	// Old reconciliation must neither extend the cooldown nor expire a new card's batch.
	if err := savePaymentRotation(v, &paymentRotation{CardFingerprint: cardFingerprint(cards[1]), NodeID: "direct", Used: 1}); err != nil {
		t.Fatal(err)
	}
	b.Until = time.Now().Add(10 * time.Minute).Unix()
	blocks[fp] = b
	if err := saveCardBlocks(v, blocks); err != nil {
		t.Fatal(err)
	}
	if err := markAuthenticationRequired(v, r); err != nil {
		t.Fatal(err)
	}
	blocks, _ = readCardBlocks(v)
	rotation, _ = readPaymentRotation(v)
	if blocks[fp] != b || rotation.Used != 1 {
		t.Fatal("reconciliation replay changed cooldown or current batch")
	}
}

func TestAuthenticationCooldownPreservesPermanentBlock(t *testing.T) {
	v := controlFixture(t)
	if err := blockPaymentCard(v, "submitted-card", "do_not_try_again"); err != nil {
		t.Fatal(err)
	}
	r := &Record{Status: "requires_action", CardFingerprint: "submitted-card", SessionID: "cs_live_Permanent", SubmittedAt: 1}
	if err := markAuthenticationRequired(v, r); err != nil {
		t.Fatal(err)
	}
	blocks, err := readCardBlocks(v)
	if err != nil {
		t.Fatal(err)
	}
	if b := blocks[r.CardFingerprint]; b.Until != 0 || b.Reason != "do_not_try_again" {
		t.Fatal("permanent block was replaced")
	}
}
