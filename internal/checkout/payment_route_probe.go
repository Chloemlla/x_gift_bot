package checkout

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
	"xgift/internal/proxy"
	"xgift/internal/vault"
)

type PaymentNodeProbe struct {
	Node       string `json:"node"`
	Type       string `json:"type"`
	Healthy    bool   `json:"healthy"`
	StripeHTTP int    `json:"stripe_http"`
	EgressIP   string `json:"egress_ip,omitempty"`
	Error      string `json:"error,omitempty"`
}

// ProbePaymentOutbounds never reads a card/key/order or assigns an order's route.
// Public unauthenticated GETs only; streamed rows contain no proxy credentials.
func ProbePaymentOutbounds(ctx context.Context, v *vault.Vault, report func(PaymentNodeProbe)) error {
	raw, err := v.Get("payment-outbounds")
	if err != nil {
		return err
	}
	defer clear(raw)
	nodes, err := proxy.ParseOutboundPool(raw)
	if err != nil {
		return err
	}
	for _, node := range nodes {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var meta struct{ Type string }
		json.Unmarshal(node, &meta)
		result := PaymentNodeProbe{Node: "node-" + outboundID(node)[:12], Type: meta.Type}
		func() {
			probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			client, close, err := proxy.OpenOutbound(probeCtx, node)
			if err != nil {
				result.Error = "node_start_failed"
				return
			}
			defer close()
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			req, _ := http.NewRequestWithContext(probeCtx, "GET", "https://api.stripe.com/", nil)
			res, err := client.Do(req)
			if err != nil {
				result.Error = "stripe_connect_failed"
				return
			}
			io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
			res.Body.Close()
			result.StripeHTTP = res.StatusCode
			result.Healthy = res.StatusCode == 404
			if !result.Healthy {
				result.Error = "unexpected_stripe_response"
				return
			}
			req, _ = http.NewRequestWithContext(probeCtx, "GET", "https://api.ipify.org", nil)
			res, err = client.Do(req)
			if err != nil {
				return
			}
			defer res.Body.Close()
			b, err := io.ReadAll(io.LimitReader(res.Body, 128))
			if err != nil {
				return
			}
			if ip := strings.TrimSpace(string(b)); net.ParseIP(ip) != nil {
				result.EgressIP = ip
			}
		}()
		// Public egress observations group nodes that share the same exit IP.
		if result.EgressIP != "" {
			if err = v.Put("payment-egress:"+outboundID(node), []byte(result.EgressIP)); err != nil {
				return err
			}
		}
		report(result)
	}
	return nil
}
