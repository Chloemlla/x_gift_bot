package proxy

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"
)

func Start(ctx context.Context, config []byte, port int) (*box.Box, error) {
	ctx = include.Context(ctx)
	var raw map[string]any
	if err := stdjson.Unmarshal(config, &raw); err != nil {
		return nil, fmt.Errorf("invalid proxy configuration")
	}
	outbounds, ok := raw["outbounds"].([]any)
	if !ok || len(outbounds) == 0 {
		return nil, fmt.Errorf("proxy configuration must contain at least one outbound")
	}
	// Only traffic forwarding is allowed: drop every section that could
	// open listeners or alter the host (API services, TUN endpoints,
	// clash/v2ray/debug controllers). Outbounds, route and dns pass through.
	delete(raw, "services")
	delete(raw, "endpoints")
	delete(raw, "experimental")
	raw["log"] = map[string]any{"disabled": true}
	raw["inbounds"] = []any{map[string]any{"type": "mixed", "listen": "127.0.0.1", "listen_port": port}}
	data, err := stdjson.Marshal(raw)
	if err != nil {
		return nil, err
	}
	opts, err := json.UnmarshalExtendedContext[option.Options](ctx, data)
	if err != nil {
		return nil, fmt.Errorf("invalid embedded sing-box options")
	}
	instance, err := box.New(box.Options{Context: ctx, Options: opts})
	if err != nil {
		return nil, fmt.Errorf("initialize embedded sing-box: %w", err)
	}
	if err = instance.Start(); err != nil {
		instance.Close()
		return nil, fmt.Errorf("start embedded sing-box: %w", err)
	}
	return instance, nil
}

func Check(ctx context.Context, port int) error {
	p, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	tr := &http.Transport{Proxy: http.ProxyURL(p), TLSHandshakeTimeout: 15 * time.Second}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 25 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, "HEAD", "https://x.com", nil)
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("local proxy is running, but HTTPS to X failed: %w", err)
	}
	defer res.Body.Close()
	fmt.Fprintf(os.Stderr, "X HTTPS reachable, HTTP status %d\n", res.StatusCode)
	return nil
}
