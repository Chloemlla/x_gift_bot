package proxy

import (
	"bytes"
	"context"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"
)

// ParseOutboundPool accepts only an array of independent outbound objects.
// It never imports subscription listeners, route rules, DNS, or selectors.
func ParseOutboundPool(raw []byte) ([]stdjson.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' || len(raw) > 1<<20 {
		return nil, errors.New("payment-outbounds must be a JSON array of outbound objects (maximum 1 MiB)")
	}
	var nodes []stdjson.RawMessage
	if stdjson.Unmarshal(raw, &nodes) != nil || len(nodes) > 128 {
		return nil, errors.New("invalid outbound array or more than 128 nodes")
	}
	seen := map[string]bool{}
	ctx := include.Context(context.Background())
	for i, node := range nodes {
		var meta struct {
			Type, Tag, Server, Detour string
			Port                      uint16 `json:"server_port"`
		}
		if stdjson.Unmarshal(node, &meta) != nil || meta.Tag == "" || len(meta.Tag) > 256 || seen[meta.Tag] || meta.Detour != "" {
			return nil, fmt.Errorf("outbound %d requires a unique tag, server and server_port, with no detour", i+1)
		}
		if meta.Type == "direct" {
			var fields map[string]stdjson.RawMessage
			stdjson.Unmarshal(node, &fields)
			if len(fields) != 2 {
				return nil, fmt.Errorf("direct outbound %d accepts only type and tag", i+1)
			}
		} else if meta.Server == "" || meta.Port == 0 {
			return nil, fmt.Errorf("outbound %d requires server and server_port", i+1)
		}
		switch meta.Type {
		case "direct", "http", "socks", "shadowsocks", "vmess", "vless", "trojan", "hysteria", "hysteria2", "tuic", "anytls":
		default:
			return nil, fmt.Errorf("outbound %d is not a supported independent proxy node", i+1)
		}
		if _, err := json.UnmarshalExtendedContext[option.Outbound](ctx, node); err != nil {
			return nil, fmt.Errorf("outbound %d has invalid sing-box fields", i+1)
		}
		seen[meta.Tag] = true
	}
	return nodes, nil
}

// OpenOutbound starts a private loopback proxy with exactly one possible exit.
// No fallback or failover is configured. close must be called after client use.
func OpenOutbound(ctx context.Context, node stdjson.RawMessage) (*http.Client, func(), error) {
	var meta struct{ Type string }
	if stdjson.Unmarshal(node, &meta) != nil {
		return nil, nil, errors.New("invalid outbound")
	}
	if meta.Type == "direct" {
		client := &http.Client{Transport: &http.Transport{Proxy: nil, TLSHandshakeTimeout: 15 * time.Second}, Timeout: 35 * time.Second}
		return client, func() { client.CloseIdleConnections() }, nil
	}
	raw, err := stdjson.Marshal(map[string]any{"outbounds": []stdjson.RawMessage{node}})
	if err != nil {
		return nil, nil, errors.New("cannot encode payment outbound")
	}
	defer clear(raw)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, errors.New("cannot reserve local payment proxy")
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	instance, err := Start(ctx, raw, port)
	if err != nil {
		return nil, nil, errors.New("cannot start payment outbound; check node fields and build with with_quic,with_utls")
	}
	p, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(p), TLSHandshakeTimeout: 15 * time.Second}, Timeout: 35 * time.Second}
	close := func() { client.CloseIdleConnections(); instance.Close() }
	return client, close, nil
}
