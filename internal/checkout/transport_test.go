package checkout

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestStripeDirectAndXProxyStaySeparate(t *testing.T) {
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
	s, err := newStripe(context.Background(), v, "1234")
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
	req, _ := http.NewRequest(http.MethodGet, "https://x.com/", nil)
	p, err := x.http.Transport.(*http.Transport).Proxy(req)
	if err != nil || p.String() != proxy.URL {
		t.Fatal("X must retain its configured proxy")
	}
	res, err := x.http.Get("http://x-egress-test.invalid/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if proxyCalls.Load() != 1 || directCalls.Load() != 2 {
		t.Fatal("X traffic did not reach its configured proxy")
	}
}
