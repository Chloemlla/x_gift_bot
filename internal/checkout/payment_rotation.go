package checkout

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"math/big"
	"regexp"
	"time"
	"xgift/internal/proxy"
	"xgift/internal/vault"
)

// PaymentBatchSize is how many consecutive payment attempts share one card+node
// pair. A decline ends the batch immediately and the next attempt rotates.
const PaymentBatchSize = 3

const paymentRotationRecord = "payment-rotation"

type paymentRotation struct {
	CardFingerprint string          `json:"card_fingerprint,omitempty"`
	NodeID          string          `json:"node_id,omitempty"`
	Outbound        json.RawMessage `json:"outbound,omitempty"`
	Used            int             `json:"used"`
	UpdatedAt       int64           `json:"updated_at"`
}

func readPaymentRotation(v *vault.Vault) (*paymentRotation, error) {
	raw, err := v.Get(paymentRotationRecord)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	var r paymentRotation
	if json.Unmarshal(raw, &r) != nil || r.Used < 0 || r.Used > PaymentBatchSize {
		return nil, errors.New("invalid payment rotation record")
	}
	if len(r.Outbound) > 0 {
		if _, err = proxy.ParseOutboundPool(append(append([]byte{'['}, r.Outbound...), ']')); err != nil {
			return nil, errors.New("invalid payment rotation outbound")
		}
		if r.NodeID != outboundID(r.Outbound) {
			return nil, errors.New("payment rotation node does not match its snapshot")
		}
	}
	return &r, nil
}

func savePaymentRotation(v *vault.Vault, r *paymentRotation) error {
	r.UpdatedAt = time.Now().Unix()
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	defer clear(b)
	return v.Put(paymentRotationRecord, b)
}

// ResetPaymentRotation expires the running batch so the next payment attempt
// selects a fresh card+node pair.
func ResetPaymentRotation(v *vault.Vault) error {
	paymentRouteMu.Lock()
	defer paymentRouteMu.Unlock()
	rotation, err := readPaymentRotation(v)
	if err != nil {
		return err
	}
	if rotation == nil {
		rotation = &paymentRotation{}
	}
	rotation.Used = PaymentBatchSize
	return savePaymentRotation(v, rotation)
}

// pickCard returns the pinned card when it is still available, otherwise the
// least recently used candidate (random tie-break) so rotation stays balanced.
func pickCard(v *vault.Vault, candidates []card, pinned string) (card, error) {
	if pinned != "" {
		for _, c := range candidates {
			if cardFingerprint(c) == pinned {
				return c, nil
			}
		}
	}
	usage, err := readCardUsage(v)
	if err != nil {
		return card{}, err
	}
	oldest := int64(-1)
	preferred := make([]card, 0, len(candidates))
	for _, c := range candidates {
		used := usage[cardFingerprint(c)]
		if oldest < 0 || used < oldest {
			oldest = used
			preferred = append(preferred[:0], c)
		} else if used == oldest {
			preferred = append(preferred, c)
		}
	}
	index, err := rand.Int(rand.Reader, big.NewInt(int64(len(preferred))))
	if err != nil {
		return card{}, errors.New("cannot choose payment card")
	}
	return preferred[index.Int64()], nil
}

// preferNode favours a different server than the last used one and drops the
// forbidden node unless it is the only option left.
func preferNode(list []json.RawMessage, lastGroup, forbidden string) []json.RawMessage {
	out := list
	if lastGroup != "" {
		filtered := make([]json.RawMessage, 0, len(out))
		for _, node := range out {
			if routeGroup(node) != lastGroup {
				filtered = append(filtered, node)
			}
		}
		if len(filtered) > 0 {
			out = filtered
		}
	}
	if len(out) > 1 && forbidden != "" {
		filtered := make([]json.RawMessage, 0, len(out))
		for _, node := range out {
			if outboundID(node) != forbidden {
				filtered = append(filtered, node)
			}
		}
		if len(filtered) > 0 {
			out = filtered
		}
	}
	return out
}

func pickNode(list []json.RawMessage) (json.RawMessage, error) {
	if len(list) == 0 {
		return nil, ErrNoUsableCard
	}
	index, err := rand.Int(rand.Reader, big.NewInt(int64(len(list))))
	if err != nil {
		return nil, errors.New("cannot choose payment node")
	}
	return list[index.Int64()], nil
}

// choosePaymentPair picks one card and one node. Whole-card blocks cool the
// card everywhere; a declined pair only cools that card+node combination so the
// same card can be retried through another node.
func choosePaymentPair(v *vault.Vault, cards []card, nodes []json.RawMessage, keepCard, forbidden string) (card, json.RawMessage, error) {
	blocks, err := readCardBlocks(v)
	if err != nil {
		return card{}, nil, err
	}
	now := time.Now().Unix()
	usable := make([]card, 0, len(cards))
	for _, c := range cards {
		if validateCard(c) != nil {
			continue
		}
		if block, ok := blocks[cardFingerprint(c)]; ok && activeCardBlock(block, now) {
			continue
		}
		usable = append(usable, c)
	}
	if len(usable) == 0 {
		return card{}, nil, ErrNoUsableCard
	}
	if len(nodes) == 0 {
		chosen, err := pickCard(v, usable, keepCard)
		if err != nil {
			return card{}, nil, err
		}
		return chosen, json.RawMessage(`{"type":"direct","tag":"direct"}`), nil
	}
	available, err := availablePaymentNodes(v, nodes)
	if err != nil {
		return card{}, nil, err
	}
	if len(available) == 0 {
		return card{}, nil, ErrPaymentNodesCooling
	}
	lastGroup := ""
	if last, e := v.Get("stripe-route:last-node"); e == nil && len(last) > 0 {
		for _, node := range nodes {
			if outboundID(node) == string(last) {
				lastGroup = routeGroup(node)
				break
			}
		}
		clear(last)
	}
	freeNodes := make(map[string][]json.RawMessage, len(usable))
	candidates := make([]card, 0, len(usable))
	for _, c := range usable {
		fingerprint := cardFingerprint(c)
		list := make([]json.RawMessage, 0, len(available))
		for _, node := range available {
			if block, ok := blocks[pairBlockKey(fingerprint, outboundID(node))]; ok && activeCardBlock(block, now) {
				continue
			}
			list = append(list, node)
		}
		if len(list) == 0 {
			continue
		}
		freeNodes[fingerprint] = preferNode(list, lastGroup, forbidden)
		candidates = append(candidates, c)
	}
	if len(candidates) == 0 {
		return card{}, nil, ErrNoUsableCard
	}
	chosen, err := pickCard(v, candidates, keepCard)
	if err != nil {
		return card{}, nil, err
	}
	node, err := pickNode(freeNodes[cardFingerprint(chosen)])
	if err != nil {
		return card{}, nil, err
	}
	return chosen, node, nil
}

func paymentNodes(v *vault.Vault) ([]json.RawMessage, error) {
	raw, err := v.Get("payment-outbounds")
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	return proxy.ParseOutboundPool(raw)
}

// continueOrRotate returns the pair the next payment attempt should use. A
// running batch of fewer than PaymentBatchSize attempts keeps its card; a new
// batch prefers a different node.
func continueOrRotate(v *vault.Vault, cards []card, nodes []json.RawMessage, rotation *paymentRotation, continueBatch bool, forbidden string) (card, json.RawMessage, int, error) {
	if continueBatch && rotation != nil && rotation.Used < PaymentBatchSize && rotation.CardFingerprint != "" {
		c, ok := cardByFingerprint(cards, rotation.CardFingerprint)
		if ok && validateCard(c) == nil {
			if blocked, e := paymentCardBlocked(v, rotation.CardFingerprint); e != nil {
				return card{}, nil, 0, e
			} else if !blocked {
				if len(rotation.Outbound) == 0 {
					return c, json.RawMessage(`{"type":"direct","tag":"direct"}`), rotation.Used + 1, nil
				}
				pairCooling, e := paymentPairBlocked(v, rotation.CardFingerprint, rotation.NodeID)
				if e != nil {
					return card{}, nil, 0, e
				}
				until, e := nodeCoolingUntil(v, rotation.Outbound)
				if e != nil {
					return card{}, nil, 0, e
				}
				if !pairCooling && until <= time.Now().Unix() {
					return c, rotation.Outbound, rotation.Used + 1, nil
				}
				// The running card keeps the batch but moves to another node
				// when its own pair or node is cooling.
				next, node, e := choosePaymentPair(v, cards, nodes, rotation.CardFingerprint, rotation.NodeID)
				if e != nil {
					return card{}, nil, 0, e
				}
				used := rotation.Used + 1
				if cardFingerprint(next) != rotation.CardFingerprint {
					used = 1
				}
				return next, node, used, nil
			}
		}
	}
	chosen, node, err := choosePaymentPair(v, cards, nodes, "", forbidden)
	if err != nil {
		return card{}, nil, 0, err
	}
	return chosen, node, 1, nil
}

// assignPaymentRoute pins the card+node pair a payment submission must use and
// records it on the order. rotate forces a fresh pair (used after a decline).
// Read-only Stripe calls keep using selectPaymentRoute and never rotate cards.
func assignPaymentRoute(v *vault.Vault, recipient string, rotate bool) (*paymentRoute, card, error) {
	if !regexp.MustCompile(`^[0-9]{1,32}$`).MatchString(recipient) {
		return nil, card{}, errors.New("payment route requires a bound recipient")
	}
	paymentRouteMu.Lock()
	defer paymentRouteMu.Unlock()
	cards, err := readCards(v)
	if err != nil {
		return nil, card{}, err
	}
	rotation, err := readPaymentRotation(v)
	if err != nil {
		return nil, card{}, err
	}
	nodes, err := paymentNodes(v)
	if err != nil {
		return nil, card{}, err
	}
	route, routeErr := readPaymentRoute(v, recipient)
	if routeErr != nil && !errors.Is(routeErr, sql.ErrNoRows) {
		return nil, card{}, routeErr
	}

	var bound card
	boundOK := false
	if routeErr == nil && route.Card != "" {
		if c, ok := cardByFingerprint(cards, route.Card); ok && validateCard(c) == nil {
			blocked, e := paymentCardBlocked(v, route.Card)
			if e != nil {
				return nil, card{}, e
			}
			if !blocked {
				bound, boundOK = c, true
			}
		}
	}

	if routeErr == nil && !rotate && boundOK {
		// The order keeps its pinned pair; only a durable node cooldown or a
		// cooling card+node pair may move it, and the card always stays with the
		// order.
		pairCooling, e := paymentPairBlocked(v, route.Card, route.NodeID)
		if e != nil {
			return nil, card{}, e
		}
		if !pairCooling {
			until, e := nodeCoolingUntil(v, route.Outbound)
			if e != nil {
				return nil, card{}, e
			}
			if until > time.Now().Unix() {
				next, e := rotateCoolingRouteLocked(v, route)
				if e != nil {
					return nil, card{}, e
				}
				return next, bound, nil
			}
			return route, bound, nil
		}
	}

	// A fresh pair is needed: first attempt, a route with no usable card, or an
	// explicit rotation after a decline. A retry keeps the rejected card out of
	// rotation for the cooldown.
	forbidden := ""
	if routeErr == nil {
		forbidden = route.NodeID
	}
	if rotate && routeErr == nil && route.Card != "" {
		if _, ok := cardByFingerprint(cards, route.Card); ok {
			if err = coolPaymentPairLocked(v, route.Card, route.NodeID, "declined"); err != nil {
				return nil, card{}, err
			}
		}
	}
	continueBatch := !rotate
	chosen, node, used, err := continueOrRotate(v, cards, nodes, rotation, continueBatch, forbidden)
	if err != nil {
		return nil, card{}, err
	}
	fingerprint := cardFingerprint(chosen)
	next := &paymentRoute{Recipient: recipient, NodeID: outboundID(node), Outbound: node, Card: fingerprint, SelectedAt: time.Now().Unix()}
	b, err := json.Marshal(next)
	if err != nil {
		return nil, card{}, err
	}
	defer clear(b)
	if routeErr == nil {
		expected, e := v.Get("stripe-route:" + recipient)
		if e != nil {
			return nil, card{}, e
		}
		defer clear(expected)
		archive := "stripe-route-history:" + recipient + ":" + time.Now().Format("20060102T150405.000000000")
		if e = v.ReplaceArchived("stripe-route:"+recipient, archive, expected, expected, b); e != nil {
			return nil, card{}, e
		}
	} else {
		inserted, e := v.PutIfAbsent("stripe-route:"+recipient, b)
		if e != nil {
			return nil, card{}, e
		}
		if !inserted {
			// Another process bound this order first; keep that binding.
			current, e := readPaymentRoute(v, recipient)
			if e != nil {
				return nil, card{}, e
			}
			c, ok := cardByFingerprint(cards, current.Card)
			if !ok {
				usable := usableCards(cards)
				if len(usable) == 0 {
					return nil, card{}, ErrNoUsableCard
				}
				c = usable[0]
			}
			return current, c, nil
		}
	}
	if rotation == nil {
		rotation = &paymentRotation{}
	}
	rotation.CardFingerprint = fingerprint
	rotation.NodeID = next.NodeID
	rotation.Outbound = append(json.RawMessage(nil), node...)
	rotation.Used = used
	if err = savePaymentRotation(v, rotation); err != nil {
		return nil, card{}, err
	}
	if err = touchCardUsageLocked(v, fingerprint); err != nil {
		return nil, card{}, err
	}
	if err = v.Put("stripe-route:last-node", []byte(next.NodeID)); err != nil {
		return nil, card{}, err
	}
	return next, chosen, nil
}

// markPaymentDecline ends the running batch and applies the cooling order the
// operator requested: the exit node (and its shared egress IP) is cooled first,
// and only a card that keeps declining on another node is cooled as well.
func markPaymentDecline(v *vault.Vault, recipient string) error {
	if recipient == "" {
		return nil
	}
	paymentRouteMu.Lock()
	defer paymentRouteMu.Unlock()
	rotation, err := readPaymentRotation(v)
	if err != nil {
		return err
	}
	if rotation == nil {
		rotation = &paymentRotation{}
	}
	if route, e := readPaymentRoute(v, recipient); e == nil {
		if route.Card != "" {
			if err = coolPaymentPairLocked(v, route.Card, route.NodeID, "declined"); err != nil {
				return err
			}
		}
		if len(route.Outbound) > 0 {
			if err = coolPaymentNodeLocked(v, route.Outbound, "declined", paymentNodeDeclineCooldown); err != nil {
				return err
			}
		}
	}
	rotation.Used = PaymentBatchSize
	return savePaymentRotation(v, rotation)
}

// cardTokenizationRejected reports whether a failed card submission was
// rejected by the provider because of the card itself rather than by a
// transport or API problem. A rejected card is rotated away immediately.
func cardTokenizationRejected(err error) bool {
	var se *stripeError
	return errors.As(err, &se) && se.Type == "card_error"
}
