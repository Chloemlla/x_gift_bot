package checkout

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
	"xgift/internal/proxy"
)

func TestNetworkFailoverOnlyReplaysSafeRequests(t *testing.T) {
	for _, tc := range []struct {
		name, method, path     string
		status                 int
		wantCalls, wantCooling int
	}{
		{"poll", "GET", "payment_pages/cs_live_Test/poll", 0, 2, 1},
		{"init", "POST", "payment_pages/cs_live_Test/init", 0, 2, 1},
		{"confirm", "POST", "payment_pages/cs_live_Test/confirm", 0, 1, 1},
		{"tokenize", "POST", "payment_methods", 0, 1, 1},
		{"decline", "POST", "payment_pages/cs_live_Test/confirm", 402, 1, 0},
		{"forbidden", "GET", "payment_pages/cs_live_Test/poll", 403, 1, 0},
		{"rate_limit", "GET", "payment_pages/cs_live_Test/poll", 429, 1, 0},
		{"server_error", "GET", "payment_pages/cs_live_Test/poll", 500, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := controlFixture(t)
			v.Put("payment-outbounds", []byte(twoPaymentNodes))
			route, e := selectPaymentRoute(v, "1234")
			if e != nil {
				t.Fatal(e)
			}
			original := route.NodeID
			calls, opens := 0, 0
			tr := stripeRoundTrip(func(*http.Request) (*http.Response, error) {
				calls++
				if calls == 1 && tc.status == 0 {
					return nil, errors.New("synthetic connection reset")
				}
				status, body := 200, `{}`
				if tc.status != 0 {
					status = tc.status
				}
				if tc.status == 402 {
					status, body = 402, `{"error":{"type":"card_error","code":"card_declined","decline_code":"generic_decline"}}`
				}
				return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			s := &stripeClient{http: &http.Client{Transport: tr}, vault: v, key: "pk_live_Test", route: route}
			s.openRoute = func(context.Context, json.RawMessage) (*http.Client, func(), error) {
				opens++
				return &http.Client{Transport: tr}, func() {}, nil
			}
			defer s.close()
			var out map[string]any
			before := time.Now()
			err := s.call(context.Background(), tc.method, tc.path, url.Values{}, "", &out)
			if calls != tc.wantCalls {
				t.Fatalf("calls=%d want=%d", calls, tc.wantCalls)
			}
			if tc.wantCalls == 2 {
				if err != nil || opens != 1 {
					t.Fatalf("safe request did not fail over: %v", err)
				}
			} else if err == nil || opens != 0 {
				t.Fatal("unsafe request replayed or lost failure")
			}
			nodes, _ := proxy.ParseOutboundPool([]byte(twoPaymentNodes))
			cooling := 0
			for _, node := range nodes {
				until, e := nodeCoolingUntil(v, node)
				if e != nil {
					t.Fatal(e)
				}
				if until > time.Now().Unix() {
					cooling++
					if until < before.Add(paymentNodeCooldown).Unix() || until > time.Now().Add(paymentNodeCooldown).Unix() {
						t.Fatal("cooldown duration wrong")
					}
				}
			}
			if cooling != tc.wantCooling {
				t.Fatalf("cooling=%d want=%d", cooling, tc.wantCooling)
			}
			saved, _ := readPaymentRoute(v, "1234")
			if (saved.NodeID != original) != (tc.wantCalls == 2) {
				t.Fatal("unexpected route mutation")
			}
		})
	}
}

const twoPaymentNodes = `[{"type":"http","tag":"a","server":"a.example.invalid","server_port":443},{"type":"http","tag":"b","server":"b.example.invalid","server_port":443}]`
