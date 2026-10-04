package checkout

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"time"
	"xgift/internal/vault"
)

const (
	paymentNodeCooldown        = 6 * time.Hour
	paymentNodeDeclineCooldown = 30 * time.Minute
)

var ErrPaymentNodesCooling = errors.New("all payment nodes are cooling down; no alternate route available")

type nodeCooldown struct {
	Until  int64  `json:"until"`
	Reason string `json:"reason"`
}

func routeGroup(node json.RawMessage) string {
	var m struct{ Type, Server string }
	json.Unmarshal(node, &m)
	if m.Type == "direct" {
		return "direct"
	}
	return "server:" + strings.ToLower(m.Server)
}
func cooldownKey(group string) string {
	d := sha256.Sum256([]byte(group))
	return "payment-node-cooldown:" + hex.EncodeToString(d[:])
}
func nodeCooldownKeys(v *vault.Vault, node json.RawMessage) ([]string, error) {
	id := outboundID(node)
	keys := []string{cooldownKey("node:" + id), cooldownKey(routeGroup(node))}
	b, e := v.Get("payment-egress:" + id)
	if errors.Is(e, sql.ErrNoRows) {
		return keys, nil
	}
	if e != nil {
		return nil, e
	}
	defer clear(b)
	ip := net.ParseIP(string(b))
	if ip == nil {
		return nil, errors.New("invalid recorded payment egress IP")
	}
	return append(keys, cooldownKey("ip:"+ip.String())), nil
}
func nodeCoolingUntil(v *vault.Vault, node json.RawMessage) (int64, error) {
	keys, e := nodeCooldownKeys(v, node)
	if e != nil {
		return 0, e
	}
	var until int64
	for _, key := range keys {
		b, e := v.Get(key)
		if errors.Is(e, sql.ErrNoRows) {
			continue
		}
		if e != nil {
			return 0, e
		}
		var c nodeCooldown
		e = json.Unmarshal(b, &c)
		clear(b)
		if e != nil {
			return 0, errors.New("invalid node cooldown record")
		}
		if c.Until > until {
			until = c.Until
		}
	}
	return until, nil
}
func coolPaymentNode(v *vault.Vault, node json.RawMessage) error {
	paymentRouteMu.Lock()
	defer paymentRouteMu.Unlock()
	return coolPaymentNodeLocked(v, node, "transport_failure", paymentNodeCooldown)
}

// coolPaymentNodeLocked cools the node, its server group and any shared egress
// IP so a flagged exit is skipped for every card. Caller must hold
// paymentRouteMu.
func coolPaymentNodeLocked(v *vault.Vault, node json.RawMessage, reason string, window time.Duration) error {
	keys, e := nodeCooldownKeys(v, node)
	if e != nil {
		return e
	}
	until := time.Now().Add(window).Unix()
	old, e := nodeCoolingUntil(v, node)
	if e != nil {
		return e
	}
	if old > until {
		until = old
	}
	b, _ := json.Marshal(nodeCooldown{Until: until, Reason: reason})
	defer clear(b)
	for _, key := range keys {
		if e = v.Put(key, b); e != nil {
			return e
		}
	}
	return nil
}
func availablePaymentNodes(v *vault.Vault, nodes []json.RawMessage) ([]json.RawMessage, error) {
	available := make([]json.RawMessage, 0, len(nodes))
	now := time.Now().Unix()
	for _, node := range nodes {
		until, e := nodeCoolingUntil(v, node)
		if e != nil {
			return nil, e
		}
		if until <= now {
			available = append(available, node)
		}
	}
	return available, nil
}
