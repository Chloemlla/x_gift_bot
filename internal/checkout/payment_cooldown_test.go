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
	"xgift/internal/proxy"
	"xgift/internal/vault"
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
					if until < before.Add(6*time.Hour).Unix() || until > time.Now().Add(6*time.Hour).Unix() {
						t.Fatal("cooldown duration not six hours")
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

func TestNodeCooldownSurvivesVaultReopen(t *testing.T) {
	dir := t.TempDir()
	password := filepath.Join(dir, "password")
	if err := os.WriteFile(password, []byte(strings.Repeat("p", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "vault.db")
	v, err := vault.Open(path, password, true)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`[{"type":"direct","tag":"direct"}]`)
	if err = v.Put("payment-outbounds", raw); err != nil {
		t.Fatal(err)
	}
	nodes, err := proxy.ParseOutboundPool(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err = coolPaymentNode(v, nodes[0]); err != nil {
		t.Fatal(err)
	}
	until, err := nodeCoolingUntil(v, nodes[0])
	if err != nil {
		t.Fatal(err)
	}
	if err = v.Close(); err != nil {
		t.Fatal(err)
	}
	v, err = vault.Open(path, password, false)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	after, err := nodeCoolingUntil(v, nodes[0])
	if err != nil || after != until {
		t.Fatalf("cooldown changed after reopen: before=%d after=%d err=%v", until, after, err)
	}
	if _, err = selectPaymentRoute(v, "1234"); !errors.Is(err, ErrPaymentNodesCooling) {
		t.Fatalf("restart bypassed cooldown: %v", err)
	}
}

func TestCooldownGroupsSharedExitAndExpires(t *testing.T) {
	v := controlFixture(t)
	raw := `[{"type":"http","tag":"a","server":"a.invalid","server_port":443},{"type":"http","tag":"b","server":"b.invalid","server_port":443},{"type":"direct","tag":"direct"}]`
	v.Put("payment-outbounds", []byte(raw))
	nodes, e := proxy.ParseOutboundPool([]byte(raw))
	if e != nil {
		t.Fatal(e)
	}
	for _, node := range nodes[:2] {
		v.Put("payment-egress:"+outboundID(node), []byte("203.0.113.10"))
	}
	if e = coolPaymentNode(v, nodes[0]); e != nil {
		t.Fatal(e)
	}
	state, e := PaymentNetworkStatus(v)
	if e != nil || state.Cooling != 2 || state.Available != 1 {
		t.Fatalf("shared IP not cooled: %+v %v", state, e)
	}
	r, e := selectPaymentRoute(v, "1234")
	if e != nil || r.NodeID != outboundID(nodes[2]) {
		t.Fatal("did not choose remaining direct exit")
	}
	keys, _ := nodeCooldownKeys(v, nodes[0])
	past, _ := json.Marshal(nodeCooldown{Until: time.Now().Add(-time.Second).Unix(), Reason: "transport_failure"})
	for _, key := range keys {
		v.Put(key, past)
	}
	state, e = PaymentNetworkStatus(v)
	if e != nil || state.Available != 3 || state.Cooling != 0 {
		t.Fatal("expired cooldown did not recover")
	}
	if e = coolPaymentNode(v, nodes[2]); e != nil {
		t.Fatal(e)
	}
	state, _ = PaymentNetworkStatus(v)
	if state.Cooling != 1 {
		t.Fatal("direct exit did not enter cooldown")
	}
}

func TestReadOnlyFailoverHasThreeAttemptLimit(t *testing.T) {
	v := controlFixture(t)
	raw := `[{"type":"http","tag":"a","server":"a.invalid","server_port":443},{"type":"http","tag":"b","server":"b.invalid","server_port":443},{"type":"http","tag":"c","server":"c.invalid","server_port":443},{"type":"direct","tag":"direct"}]`
	v.Put("payment-outbounds", []byte(raw))
	route, e := selectPaymentRoute(v, "1234")
	if e != nil {
		t.Fatal(e)
	}
	calls := 0
	tr := stripeRoundTrip(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("synthetic timeout") })
	s := &stripeClient{http: &http.Client{Transport: tr}, vault: v, key: "pk_live_Test", route: route}
	defer s.close()
	s.openRoute = func(context.Context, json.RawMessage) (*http.Client, func(), error) {
		return &http.Client{Transport: tr}, func() {}, nil
	}
	var out map[string]any
	if e = s.call(context.Background(), "GET", "payment_pages/cs_live_Test/poll", url.Values{}, "", &out); e == nil {
		t.Fatal("missing failure")
	}
	state, _ := PaymentNetworkStatus(v)
	if calls != 3 || state.Cooling != 3 || state.Available != 1 {
		t.Fatalf("retry budget violated: calls=%d state=%+v", calls, state)
	}
}

func TestNoAvailableExitAndCanceledRequestDoNotReplay(t *testing.T) {
	v := controlFixture(t)
	raw := `[{"type":"direct","tag":"direct"}]`
	v.Put("payment-outbounds", []byte(raw))
	v.Put("stripe-key", []byte("pk_live_Test"))
	s, e := newStripe(context.Background(), v, "1234")
	if e != nil {
		t.Fatal(e)
	}
	defer s.close()
	if s.http.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("explicit direct exit used proxy")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out map[string]any
	s.call(ctx, "GET", "payment_pages/cs_live_Test/poll", url.Values{}, "", &out)
	state, _ := PaymentNetworkStatus(v)
	if state.Cooling != 0 {
		t.Fatal("user cancellation cooled node")
	}
	if e = coolPaymentNode(v, s.route.Outbound); e != nil {
		t.Fatal(e)
	}
	if _, e = selectPaymentRoute(v, "1234"); !errors.Is(e, ErrPaymentNodesCooling) {
		t.Fatal("bound order bypassed cooling")
	}
	if _, e = selectPaymentRoute(v, "5678"); !errors.Is(e, ErrPaymentNodesCooling) {
		t.Fatal("new order bypassed cooling")
	}
}
