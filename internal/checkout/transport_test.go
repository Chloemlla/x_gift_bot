package checkout

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestXIdentityDirectAndRegionalCheckoutProxyStaySeparate(t *testing.T) {
	var proxyCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer proxy.Close()
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		t.Setenv(key, proxy.URL)
	}
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	v := controlFixture(t)
	for name, value := range map[string]string{
		"stripe-key": "pk_live_Test",
		"api-auth":   `{"Authorization":"Bearer synthetic","UserAgent":"test"}`,
		"cookies":    `{"cookies":[{"Name":"auth_token","Value":"synthetic","Domain":"x.com"},{"Name":"ct0","Value":"synthetic","Domain":"x.com"}]}`,
	} {
		if err := v.Put(name, []byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	s, err := newStripe(context.Background(), v, "1234", paymentRead)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	tr := s.http.Transport.(*http.Transport)
	if tr.Proxy != nil {
		t.Fatal("Stripe must never select an application or environment proxy")
	}
	var directCalls atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	tr.TLSClientConfig = target.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		req, _ := http.NewRequest(method, target.URL, nil)
		res, err := s.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusNoContent {
			t.Fatal("unexpected direct response")
		}
	}
	if directCalls.Load() != 2 || proxyCalls.Load() != 0 {
		t.Fatal("Stripe traffic did not stay direct")
	}
	_, portText, _ := net.SplitHostPort(strings.TrimPrefix(proxy.URL, "http://"))
	port, _ := strconv.Atoi(portText)
	x, err := newXClient(v, port)
	if err != nil {
		t.Fatal(err)
	}
	defer x.close()
	xtr := x.http.Transport.(*http.Transport)
	if xtr.Proxy != nil {
		t.Fatal("X must ignore both the legacy proxy port and environment proxies")
	}
	xtr.TLSClientConfig = target.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		req, _ := http.NewRequest(method, target.URL, nil)
		res, err := x.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusNoContent {
			t.Fatal("unexpected X direct response")
		}
	}
	if proxyCalls.Load() != 0 || directCalls.Load() != 4 {
		t.Fatal("X traffic did not stay direct")
	}
	request, _ := http.NewRequest(http.MethodGet, "https://x.com/", nil)
	configured, err := x.regionalHTTP.Transport.(*http.Transport).Proxy(request)
	if err != nil || configured.String() != proxy.URL {
		t.Fatal("creation lost the configured X proxy")
	}
	res, err := x.regionalHTTP.Get("http://x-egress-test.invalid/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if proxyCalls.Load() != 1 {
		t.Fatal("creation transport did not use the configured proxy")
	}

}

func TestXOperationsChooseTheirRequiredRoute(t *testing.T) {
	v := controlFixture(t)
	var reads, creates []string
	route := func(paths *[]string, fail bool) *http.Client {
		return &http.Client{Transport: mockXTransport(func(r *http.Request) (*http.Response, error) {
			*paths = append(*paths, r.Method+" "+r.URL.Path)
			if fail {
				return nil, errors.New("proxy unavailable")
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"data":{}}`))}, nil
		})}
	}
	c := &xClient{vault: v, headers: http.Header{}, http: route(&reads, false), regionalHTTP: route(&creates, false)}
	for _, operation := range []struct {
		name     string
		mutation bool
	}{
		{"PremiumGiftingQuery", false},
		{"useSubscriptionProductDetailsByRestIdQuery", false},
		{"useOneTimePurchaseGiftMutation", true},
	} {
		var out json.RawMessage
		if err := c.call(context.Background(), "fixture", operation.name, "fixture", map[string]string{}, operation.mutation, &out); err != nil {
			t.Fatal(err)
		}
	}
	if len(reads) != 1 || len(creates) != 2 || !strings.Contains(creates[1], "POST /i/api/graphql/fixture/useOneTimePurchaseGiftMutation") {
		t.Fatalf("reads=%v creates=%v", reads, creates)
	}
	c.regionalHTTP = route(&creates, true)
	var out json.RawMessage
	if err := c.call(context.Background(), "fixture", "useOneTimePurchaseGiftMutation", "fixture", map[string]string{}, true, &out); err == nil {
		t.Fatal("failed proxy must stop creation")
	}
	if len(reads) != 1 || len(creates) != 3 {
		t.Fatal("creation retried or fell back to direct")
	}
	if err := c.call(context.Background(), "fixture", "PremiumGiftingQuery", "fixture", map[string]string{}, false, &out); err != nil {
		t.Fatal("proxy failure blocked direct reads", err)
	}
}

func TestXQuoteUsesSameRegionAsCheckout(t *testing.T) {
	var directCalls, regionalCalls int
	for _, months := range []int{3, 6} {
		p := Plan{Months: months, Minor: months * 10000, Currency: "bdt", ProductID: "synthetic-product"}
		transport := func(regional bool) *http.Client {
			return &http.Client{Transport: mockXTransport(func(r *http.Request) (*http.Response, error) {
				currency, amount := "Myr", int64(102000000)
				if regional {
					regionalCalls++
					currency = "Bdt"
					amount = int64(p.Minor) * 10000
				} else {
					directCalls++
				}
				body, _ := json.Marshal(map[string]any{"data": map[string]any{"web_subscription_product_details_by_rest_id": map[string]any{"rest_id": p.ProductID, "prices": []any{map[string]any{"currency_code": currency, "amount_local_micro": amount, "price_type": "OneTime"}}}}})
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})}
		}
		c := &xClient{http: transport(false), regionalHTTP: transport(true), headers: http.Header{}}
		if err := c.quote(context.Background(), "fixture", p); err != nil {
			t.Fatalf("%d months: %v", months, err)
		}
	}
	if directCalls != 0 || regionalCalls != 2 {
		t.Fatalf("price routing: direct=%d regional=%d", directCalls, regionalCalls)
	}
}
