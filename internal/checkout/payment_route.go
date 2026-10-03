package checkout

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"regexp"
	"sync"
	"time"
	"xgift/internal/proxy"
	"xgift/internal/vault"
)

var paymentRouteMu sync.Mutex

type paymentRoute struct {
	Recipient  string          `json:"recipient"`
	NodeID     string          `json:"node_id"`
	Outbound   json.RawMessage `json:"outbound"`
	SelectedAt int64           `json:"selected_at"`
}

type PaymentNetwork struct {
	Mode      string `json:"mode"`
	Nodes     int    `json:"nodes"`
	Available int    `json:"available"`
	Cooling   int    `json:"cooling"`
}

func PaymentNetworkStatus(v *vault.Vault) (PaymentNetwork, error) {
	raw, err := v.Get("payment-outbounds")
	if errors.Is(err, sql.ErrNoRows) {
		return PaymentNetwork{Mode: "direct"}, nil
	}
	if err != nil {
		return PaymentNetwork{}, err
	}
	defer clear(raw)
	nodes, err := proxy.ParseOutboundPool(raw)
	if err != nil {
		return PaymentNetwork{}, err
	}
	mode := "direct"
	if len(nodes) > 0 {
		mode = "pool"
	}
	available, err := availablePaymentNodes(v, nodes)
	if err != nil {
		return PaymentNetwork{}, err
	}
	return PaymentNetwork{Mode: mode, Nodes: len(nodes), Available: len(available), Cooling: len(nodes) - len(available)}, nil
}

func outboundID(raw json.RawMessage) string {
	// Decode/re-encode to ignore whitespace and object key ordering.
	var node map[string]any
	json.Unmarshal(raw, &node)
	b, _ := json.Marshal(node)
	defer clear(b)
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:])
}

func readPaymentRoute(v *vault.Vault, recipient string) (*paymentRoute, error) {
	raw, err := v.Get("stripe-route:" + recipient)
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	var route paymentRoute
	if json.Unmarshal(raw, &route) != nil || route.Recipient != recipient || route.SelectedAt <= 0 || len(route.Outbound) == 0 || route.NodeID != outboundID(route.Outbound) {
		return nil, errors.New("saved payment route is invalid; refusing to select another node")
	}
	if _, err = proxy.ParseOutboundPool(append(append([]byte{'['}, route.Outbound...), ']')); err != nil {
		return nil, errors.New("saved payment outbound is invalid")
	}
	return &route, nil
}

// PaymentNodeLabel reads routing evidence without selecting or contacting a node.
func PaymentNodeLabel(v *vault.Vault, recipient string) (string, error) {
	r, err := readPaymentRoute(v, recipient)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if routeGroup(r.Outbound) == "direct" {
		return "direct", nil
	}
	return "node-" + r.NodeID[:12], nil
}

// A customer's bound order retains its encrypted outbound snapshot across link
// replacement, restarts and pool edits unless a transport failure cooled the node.
// A card decline never selects another node.
func selectPaymentRoute(v *vault.Vault, recipient string) (*paymentRoute, error) {
	if !regexp.MustCompile(`^[0-9]{1,32}$`).MatchString(recipient) {
		return nil, errors.New("payment route requires a bound recipient")
	}
	paymentRouteMu.Lock()
	defer paymentRouteMu.Unlock()
	route, err := readPaymentRoute(v, recipient)
	if err == nil {
		until, e := nodeCoolingUntil(v, route.Outbound)
		if e != nil {
			return nil, e
		}
		if until > time.Now().Unix() {
			return rotateCoolingRouteLocked(v, route)
		}
		return route, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	raw, err := v.Get("payment-outbounds")
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	nodes, err := proxy.ParseOutboundPool(raw)
	if err != nil || len(nodes) == 0 {
		return nil, err
	}
	node, err := chooseAvailablePaymentNode(v, nodes)
	if err != nil {
		return nil, err
	}
	route = &paymentRoute{Recipient: recipient, NodeID: outboundID(node), Outbound: node, SelectedAt: time.Now().Unix()}
	b, err := json.Marshal(route)
	if err != nil {
		return nil, err
	}
	defer clear(b)
	inserted, err := v.PutIfAbsent("stripe-route:"+recipient, b)
	if err != nil {
		return nil, err
	}
	if !inserted {
		return readPaymentRoute(v, recipient)
	}
	if err = v.Put("stripe-route:last-node", []byte(route.NodeID)); err != nil {
		return nil, err
	}
	return route, nil
}

func chooseAvailablePaymentNode(v *vault.Vault, nodes []json.RawMessage) (json.RawMessage, error) {
	nodes, err := availablePaymentNodes(v, nodes)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, ErrPaymentNodesCooling
	}
	last, err := v.Get("stripe-route:last-node")
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	defer clear(last)
	choices := make([]json.RawMessage, 0, len(nodes))
	serverOf := routeGroup
	lastServer := ""
	for _, node := range nodes {
		if outboundID(node) == string(last) {
			lastServer = serverOf(node)
			break
		}
	}
	for _, node := range nodes {
		if lastServer == "" || serverOf(node) != lastServer {
			choices = append(choices, node)
		}
	}
	if len(choices) == 0 {
		for _, node := range nodes {
			if len(nodes) == 1 || outboundID(node) != string(last) {
				choices = append(choices, node)
			}
		}
		if len(choices) == 0 {
			choices = nodes
		}
	}
	index, err := rand.Int(rand.Reader, big.NewInt(int64(len(choices))))
	if err != nil {
		return nil, errors.New("cannot choose payment node")
	}
	return choices[index.Int64()], nil
}

// Only a durable transport-failure cooldown permits an automatic route change.
func rotateCoolingRouteLocked(v *vault.Vault, old *paymentRoute) (*paymentRoute, error) {
	until, err := nodeCoolingUntil(v, old.Outbound)
	if err != nil {
		return nil, err
	}
	if until <= time.Now().Unix() {
		return nil, errors.New("payment node is not cooling; refusing automatic switch")
	}
	expected, err := v.Get("stripe-route:" + old.Recipient)
	if err != nil {
		return nil, err
	}
	defer clear(expected)
	var current paymentRoute
	if json.Unmarshal(expected, &current) != nil || current.NodeID != old.NodeID {
		return nil, errors.New("payment route changed during retry")
	}
	raw, err := v.Get("payment-outbounds")
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	nodes, err := proxy.ParseOutboundPool(raw)
	if err != nil {
		return nil, err
	}
	node, err := chooseAvailablePaymentNode(v, nodes)
	if err != nil {
		return nil, err
	}
	next := &paymentRoute{Recipient: old.Recipient, NodeID: outboundID(node), Outbound: node, SelectedAt: time.Now().Unix()}
	b, err := json.Marshal(next)
	if err != nil {
		return nil, err
	}
	defer clear(b)
	archive := "stripe-route-history:" + old.Recipient + ":" + time.Now().Format("20060102T150405.000000000")
	if err = v.ReplaceArchived("stripe-route:"+old.Recipient, archive, expected, expected, b); err != nil {
		return nil, err
	}
	if err = v.Put("stripe-route:last-node", []byte(next.NodeID)); err != nil {
		return nil, err
	}
	return next, nil
}

func rotateCoolingRoute(v *vault.Vault, old *paymentRoute) (*paymentRoute, error) {
	paymentRouteMu.Lock()
	defer paymentRouteMu.Unlock()
	return rotateCoolingRouteLocked(v, old)
}
