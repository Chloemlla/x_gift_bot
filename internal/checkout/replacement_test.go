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

const failedReplacementPoll = `{"session_id":"cs_live_Test","livemode":true,"is_sandbox_merchant":false,"mode":"payment","success_url":"https://x.com/recipient/gift-premium/success","state":"active","payment_object_status":"requires_payment_method"}`

func TestExpiredRecoveryCreatesNewLinkWithoutPayment(t *testing.T) {
	for _, test := range []string{"verified", "unverified", "unknown", "already_paid", "wrong_cached_order", "ineligible"} {
		t.Run(test, func(t *testing.T) {
			v, r, plan, proof := manualFixture(t)
			if err := v.Put("stripe-result:"+r.SessionID, []byte(failedReplacementPoll)); err != nil {
				t.Fatal(err)
			}
			if test == "unknown" {
				r.LastError = nil
				r.Status = "unknown"
				save(v, r)
			}
			if test == "wrong_cached_order" {
				v.Put("stripe-result:"+r.SessionID, []byte(strings.ReplaceAll(failedReplacementPoll, "cs_live_Test", "cs_live_Other")))
			}
			created, payments := 0, 0
			s := &stripeClient{vault: v, key: "pk_live_Test", http: &http.Client{Transport: stripeRoundTrip(func(req *http.Request) (*http.Response, error) {
				status := 400
				body := `{"error":{"type":"invalid_request_error","code":"checkout_not_active_session","message":"This Checkout Session is no longer active."}}`
				if strings.HasSuffix(req.URL.Path, "/confirm") || strings.HasSuffix(req.URL.Path, "/payment_methods") {
					payments++
					t.Fatal("link generation attempted payment")
				}
				if test == "already_paid" {
					status = 200
					body = strings.ReplaceAll(strings.ReplaceAll(failedReplacementPoll, "requires_payment_method", "succeeded"), `"active"`, `"succeeded"`)
				}
				if strings.Contains(req.URL.Path, "cs_live_New") {
					status = 200
					var page map[string]any
					json.Unmarshal(proof.Page, &page)
					page["session_id"] = "cs_live_New"
					page["payment_intent"] = nil
					b, _ := json.Marshal(page)
					body = string(b)
				}
				return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}}
			result, err := prepareRecoveryLink(context.Background(), v, r, s, plan, test != "unverified", func() error {
				if test == "ineligible" {
					return ErrNotEligible
				}
				return nil
			}, func() (string, string, error) {
				created++
				return "cs_live_New", "https://checkout.stripe.com/a/pay/cs_live_New", nil
			})
			if payments != 0 {
				t.Fatal("payment submitted")
			}
			switch test {
			case "verified":
				if err != nil || created != 1 || result.Status != "created" || result.PreviousSession != "cs_live_Test" || result.ReplacementCount != 1 || !unsubmitted(result) {
					t.Fatalf("bad replacement result=%+v err=%v", result, err)
				}
				raw, e := v.Get("replacement-original:cs_live_Test")
				if e != nil {
					t.Fatal(e)
				}
				var audit struct {
					Original Record `json:"original"`
					Verified bool   `json:"operator_verified_unpaid"`
				}
				json.Unmarshal(raw, &audit)
				if !audit.Verified || audit.Original.ConfirmKey != r.ConfirmKey {
					t.Fatal("lost original payment evidence")
				}
				// Reusing the old record after a replacement must not create another link.
				if _, e = replaceRecoveryLink(context.Background(), v, r, s, plan, func() (string, string, error) { created++; return "", "", nil }); e == nil || created != 1 {
					t.Fatal("stale replacement accepted")
				}
			case "already_paid":
				if err != nil || result.Status != "succeeded" || created != 0 {
					t.Fatal("paid order replaced")
				}
			case "unverified":
				if !errors.Is(err, ErrVerifyUnpaid) || created != 0 {
					t.Fatal("missing attestation accepted")
				}
			default:
				if err == nil || created != 0 {
					t.Fatalf("unsafe replacement for %s", test)
				}
			}
		})
	}
}
func TestReplacementCreationCrashLeavesUnpaidReservation(t *testing.T) {
	v, r, plan, _ := manualFixture(t)
	_, err := replaceRecoveryLink(context.Background(), v, r, nil, plan, func() (string, string, error) { return "", "", errors.New("lost creation response") })
	if err == nil {
		t.Fatal("expected creation error")
	}
	raw, e := v.Get("checkout:1234")
	if e != nil {
		t.Fatal(e)
	}
	var current Record
	json.Unmarshal(raw, &current)
	if current.Status != "creating" || !unsubmitted(&current) || current.PreviousSession != r.SessionID {
		t.Fatal("lost durable replacement reservation")
	}
	if _, err = v.Get("replacement-original:" + r.SessionID); err != nil {
		t.Fatal("old payment evidence lost")
	}
}
