package checkout

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

const twoPaymentNodes = `[{"type":"http","tag":"a","server":"a.example.invalid","server_port":443},{"type":"http","tag":"b","server":"b.example.invalid","server_port":443}]`

func TestPaymentRoutePinsAcrossPoolChangesAndCustomersRotate(t *testing.T) {
	v := controlFixture(t)
	if err := v.Put("payment-outbounds", []byte(twoPaymentNodes)); err != nil {
		t.Fatal(err)
	}
	first, err := selectPaymentRoute(v, "1234")
	if err != nil {
		t.Fatal(err)
	}
	second, err := selectPaymentRoute(v, "5678")
	if err != nil {
		t.Fatal(err)
	}
	if first.NodeID == second.NodeID {
		t.Fatal("adjacent orders reused same server with alternative available")
	}
	before, _ := v.Get("stripe-route:1234")
	v.Put("payment-outbounds", []byte(`[]`))
	same, err := selectPaymentRoute(v, "1234")
	if err != nil || same.NodeID != first.NodeID {
		t.Fatal("pool edit changed pinned order")
	}
	next, err := selectPaymentRoute(v, "9999")
	if err != nil || next != nil {
		t.Fatal("empty pool should be direct for new orders")
	}
	after, _ := v.Get("stripe-route:1234")
	if string(before) != string(after) {
		t.Fatal("existing binding rewritten")
	}
	v.Put("payment-outbounds", []byte(`{"outbounds":[]}`))
	if _, err = selectPaymentRoute(v, "7777"); err == nil {
		t.Fatal("invalid pool silently fell back")
	}
	v.Put("stripe-route:1234", []byte(`{}`))
	if _, err = selectPaymentRoute(v, "1234"); err == nil {
		t.Fatal("corrupt binding silently replaced")
	}
}

func TestConcurrentPaymentRouteSelectionKeepsOneBinding(t *testing.T) {
	v := controlFixture(t)
	v.Put("payment-outbounds", []byte(twoPaymentNodes))
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := selectPaymentRoute(v, "1234")
			if e != nil {
				t.Error(e)
				return
			}
			ids <- r.NodeID
		}()
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("concurrent selection split order across nodes")
		}
	}
}

func TestStripePoolUsesPinnedProxyAndDoesNotFallback(t *testing.T) {
	var tunnels atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer target.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "CONNECT" {
			t.Error("expected CONNECT")
			w.WriteHeader(400)
			return
		}
		if r.Host != strings.TrimPrefix(target.URL, "https://") {
			t.Error("unexpected target")
			w.WriteHeader(400)
			return
		}
		dest, err := net.Dial("tcp", r.Host)
		if err != nil {
			w.WriteHeader(502)
			return
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			dest.Close()
			return
		}
		tunnels.Add(1)
		fmt.Fprint(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
		go func() { defer conn.Close(); defer dest.Close(); io.Copy(dest, conn) }()
		io.Copy(conn, dest)
	}))
	defer upstream.Close()
	host, portText, _ := net.SplitHostPort(strings.TrimPrefix(upstream.URL, "http://"))
	port, _ := strconv.Atoi(portText)
	nodes, _ := json.Marshal([]map[string]any{{"type": "http", "tag": "local-proxy", "server": host, "server_port": port}})
	v := controlFixture(t)
	v.Put("payment-outbounds", nodes)
	v.Put("stripe-key", []byte("pk_live_Test"))
	s, err := newStripe(context.Background(), v, "1234", paymentRead)
	if err != nil {
		t.Fatal(err)
	}
	s.http.Transport.(*http.Transport).TLSClientConfig = target.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	for _, method := range []string{"GET", "POST"} {
		req, _ := http.NewRequest(method, target.URL, nil)
		res, e := s.http.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		res.Body.Close()
		if res.StatusCode != 204 {
			t.Fatal("bad proxy response")
		}
	}
	s.close()
	if tunnels.Load() == 0 {
		t.Fatal("pool traffic bypassed proxy")
	}
	pinned, _ := v.Get("stripe-route:1234")
	upstream.Close()
	v.Put("payment-outbounds", []byte(`[]`))
	s, err = newStripe(context.Background(), v, "1234", paymentRead)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	s.http.Transport.(*http.Transport).TLSClientConfig = target.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	res, err := s.http.Get(target.URL)
	if err == nil {
		res.Body.Close()
		t.Fatal("failed proxy fell back to direct")
	}
	after, _ := v.Get("stripe-route:1234")
	if string(pinned) != string(after) {
		t.Fatal("failure changed node")
	}
}
