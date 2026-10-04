package checkout

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"xgift/internal/vault"
)

// Cards are stored as an encrypted JSON array under "cards". A single legacy
// "card" record keeps working for vaults that never added another card.
const (
	cardsRecord     = "cards"
	legacyCardKey   = "card"
	cardBlockRecord = "payment-card-blocks"
)

// paymentCardCooldown is how long a declined card+node pair stays out of
// rotation while another node for the same card is available. A second decline
// of the same card on a different node cools the whole card for the same
// window. A provider no-retry instruction blocks the card until an operator
// explicitly unblocks it.
const paymentCardCooldown = 30 * time.Minute

var ErrNoUsableCard = errors.New("no usable payment card is available; every configured card may be blocked or cooling down")

var (
	cardNumberPattern = regexp.MustCompile(`^[0-9]{12,19}$`)
	cardMonthPattern  = regexp.MustCompile(`^(0[1-9]|1[0-2])$`)
	cardYearPattern   = regexp.MustCompile(`^[0-9]{4}$`)
	cardCVCPattern    = regexp.MustCompile(`^[0-9]{3,4}$`)
	cardCountry       = regexp.MustCompile(`^[A-Z]{2}$`)
)

// validateCard is the structural and expiry check every payment card must pass.
func validateCard(c card) error {
	if !cardNumberPattern.MatchString(c.Number) || !cardMonthPattern.MatchString(c.Month) || !cardYearPattern.MatchString(c.Year) || !cardCVCPattern.MatchString(c.CVC) || strings.TrimSpace(c.Name) == "" || !strings.Contains(c.Email, "@") {
		return errors.New("card, cardholder name or email is incomplete")
	}
	sum := 0
	for i, n := range c.Number {
		d := int(n - '0')
		if (len(c.Number)-i)%2 == 0 {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	if sum%10 != 0 {
		return errors.New("card number checksum is invalid")
	}
	year, _ := strconv.Atoi(c.Year)
	month, _ := strconv.Atoi(c.Month)
	if !time.Now().Before(time.Date(year, time.Month(month)+1, 1, 0, 0, 0, 0, time.UTC)) {
		return errors.New("card has expired")
	}
	if c.Country == "" {
		return errors.New("Stripe requires a billing address; supply the card billing country and applicable address fields")
	}
	if !cardCountry.MatchString(c.Country) {
		return errors.New("invalid supplied billing country")
	}
	return nil
}

// cardFingerprint identifies one card without ever exposing its number.
func cardFingerprint(c card) string {
	sum := sha256.Sum256([]byte(c.Number + ":" + c.Month + ":" + c.Year))
	return hex.EncodeToString(sum[:])
}

// cardSetFingerprint covers the whole configured set so a changed set resets
// circuit state and invalidates admin recovery previews, while rotation across
// the same set stays stable.
func cardSetFingerprint(cards []card) string {
	parts := make([]string, 0, len(cards))
	for _, c := range cards {
		parts = append(parts, cardFingerprint(c))
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])
}

func cardTail(c card) string {
	if len(c.Number) < 4 {
		return "****"
	}
	return c.Number[len(c.Number)-4:]
}

// readCards prefers the multi-card record and falls back to the legacy single
// card so existing vaults keep paying without a migration step.
func readCards(v *vault.Vault) ([]card, error) {
	raw, err := v.Get(cardsRecord)
	if err == nil {
		defer clear(raw)
		var cards []card
		if json.Unmarshal(raw, &cards) != nil || len(cards) == 0 {
			return nil, errors.New("invalid card set record")
		}
		return cards, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	raw, err = v.Get(legacyCardKey)
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	var c card
	if json.Unmarshal(raw, &c) != nil {
		return nil, errors.New("invalid card record")
	}
	return []card{c}, nil
}

// readCard keeps the legacy single-card helper working: it returns the first
// card of the configured set, fully validated.
func readCard(v *vault.Vault) (card, error) {
	cards, err := readCards(v)
	if err != nil {
		return card{}, err
	}
	c := cards[0]
	if err = validateCard(c); err != nil {
		return c, err
	}
	return c, nil
}

// legacyCardFingerprint was used while the historic single card was assumed to
// be the one that declined old orders; multi-card rotation now tracks real
// decline evidence instead.

// readCardsOptional behaves like readCards but reports an unconfigured card
// set as empty, which lets `cards add` create the first record.
func readCardsOptional(v *vault.Vault) ([]card, error) {
	cards, err := readCards(v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return cards, err
}

func usableCards(cards []card) []card {
	usable := make([]card, 0, len(cards))
	for _, c := range cards {
		if validateCard(c) == nil {
			usable = append(usable, c)
		}
	}
	return usable
}

func cardByFingerprint(cards []card, fingerprint string) (card, bool) {
	for _, c := range cards {
		if cardFingerprint(c) == fingerprint {
			return c, true
		}
	}
	return card{}, false
}

type cardBlock struct {
	Reason    string `json:"reason"`
	BlockedAt int64  `json:"blocked_at"`
	Until     int64  `json:"until,omitempty"`
}

func readCardBlocks(v *vault.Vault) (map[string]cardBlock, error) {
	blocks := map[string]cardBlock{}
	raw, err := v.Get(cardBlockRecord)
	if errors.Is(err, sql.ErrNoRows) {
		return blocks, nil
	}
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	if json.Unmarshal(raw, &blocks) != nil {
		return nil, errors.New("invalid card block record")
	}
	return blocks, nil
}

func saveCardBlocks(v *vault.Vault, blocks map[string]cardBlock) error {
	b, err := json.Marshal(blocks)
	if err != nil {
		return err
	}
	defer clear(b)
	return v.Put(cardBlockRecord, b)
}

const cardUsageRecord = "payment-card-last-used"

func readCardUsage(v *vault.Vault) (map[string]int64, error) {
	usage := map[string]int64{}
	raw, err := v.Get(cardUsageRecord)
	if errors.Is(err, sql.ErrNoRows) {
		return usage, nil
	}
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	if json.Unmarshal(raw, &usage) != nil {
		return nil, errors.New("invalid card usage record")
	}
	return usage, nil
}

// touchCardUsageLocked records when a card was handed to a payment attempt.
// Caller must hold paymentRouteMu.
func touchCardUsageLocked(v *vault.Vault, fingerprint string) error {
	if fingerprint == "" {
		return nil
	}
	usage, err := readCardUsage(v)
	if err != nil {
		return err
	}
	usage[fingerprint] = time.Now().UnixNano()
	b, err := json.Marshal(usage)
	if err != nil {
		return err
	}
	defer clear(b)
	return v.Put(cardUsageRecord, b)
}

// pairBlockKey scopes a cooldown to one card+node combination so the same card
// can be retried through a different node.
func pairBlockKey(fingerprint, nodeID string) string { return fingerprint + ":" + nodeID }

func activeCardBlock(block cardBlock, now int64) bool {
	return block.Until == 0 || block.Until > now
}

// paymentCardBlocked reports whether the whole card is currently out of
// rotation, either permanently blocked or inside a card-level cooldown.
func paymentCardBlocked(v *vault.Vault, fingerprint string) (bool, error) {
	blocks, err := readCardBlocks(v)
	if err != nil {
		return false, err
	}
	block, blocked := blocks[fingerprint]
	if !blocked {
		return false, nil
	}
	return activeCardBlock(block, time.Now().Unix()), nil
}

// paymentPairBlocked reports whether this card+node combination is cooling.
func paymentPairBlocked(v *vault.Vault, fingerprint, nodeID string) (bool, error) {
	if fingerprint == "" || nodeID == "" {
		return false, nil
	}
	blocks, err := readCardBlocks(v)
	if err != nil {
		return false, err
	}
	block, blocked := blocks[pairBlockKey(fingerprint, nodeID)]
	return blocked && activeCardBlock(block, time.Now().Unix()), nil
}

// blockPaymentCard excludes one card from rotation until an operator unblocks
// it or changes the card set. Caller must not hold paymentRouteMu.
func blockPaymentCard(v *vault.Vault, fingerprint, reason string) error {
	if fingerprint == "" {
		return nil
	}
	paymentRouteMu.Lock()
	defer paymentRouteMu.Unlock()
	blocks, err := readCardBlocks(v)
	if err != nil {
		return err
	}
	blocks[fingerprint] = cardBlock{Reason: reason, BlockedAt: time.Now().Unix()}
	return saveCardBlocks(v, blocks)
}

// coolPaymentCard keeps a declined card out of rotation for the cooldown while
// leaving any permanent provider block in place. Caller must not hold
// paymentRouteMu.
func coolPaymentCard(v *vault.Vault, fingerprint, reason string) error {
	if fingerprint == "" {
		return nil
	}
	paymentRouteMu.Lock()
	defer paymentRouteMu.Unlock()
	return coolPaymentCardLocked(v, fingerprint, reason)
}

func coolPaymentCardLocked(v *vault.Vault, fingerprint, reason string) error {
	if fingerprint == "" {
		return nil
	}
	blocks, err := readCardBlocks(v)
	if err != nil {
		return err
	}
	if existing, ok := blocks[fingerprint]; ok && existing.Until == 0 {
		return nil
	}
	now := time.Now()
	blocks[fingerprint] = cardBlock{Reason: reason, BlockedAt: now.Unix(), Until: now.Add(paymentCardCooldown).Unix()}
	return saveCardBlocks(v, blocks)
}

// coolPaymentPairLocked cools one card+node combination. When the same card has
// declined on two distinct nodes inside the window, the whole card is cooled so
// a bad card cannot be tested across the whole node pool. Caller must hold
// paymentRouteMu.
func coolPaymentPairLocked(v *vault.Vault, fingerprint, nodeID, reason string) error {
	if fingerprint == "" || nodeID == "" {
		return nil
	}
	blocks, err := readCardBlocks(v)
	if err != nil {
		return err
	}
	now := time.Now()
	until := now.Add(paymentCardCooldown).Unix()
	blocks[pairBlockKey(fingerprint, nodeID)] = cardBlock{Reason: reason, BlockedAt: now.Unix(), Until: until}
	distinct := 0
	for key, block := range blocks {
		if strings.HasPrefix(key, fingerprint+":") && block.Until > now.Unix() {
			distinct++
		}
	}
	if distinct >= 2 {
		if existing, ok := blocks[fingerprint]; !ok || existing.Until != 0 {
			blocks[fingerprint] = cardBlock{Reason: "declined_multi_node", BlockedAt: now.Unix(), Until: until}
		}
	}
	return saveCardBlocks(v, blocks)
}

// UnblockPaymentCards clears every durable card block. It is an explicit
// operator action; the recovery batch confirmation never calls it. When the
// provider no-retry instruction paused the site and a usable card remains, the
// pause is lifted together with the block.
func UnblockPaymentCards(v *vault.Vault) (int, error) {
	paymentRouteMu.Lock()
	defer paymentRouteMu.Unlock()
	blocks, err := readCardBlocks(v)
	if err != nil {
		return 0, err
	}
	if len(blocks) > 0 {
		if err = v.Put(cardBlockRecord, []byte("{}")); err != nil {
			return 0, err
		}
	}
	state, err := readPaymentControl(v)
	if err != nil {
		return 0, err
	}
	if state.Paused && state.Reason == "do_not_try_again" {
		usable, e := hasUsableCard(v)
		if e != nil {
			return 0, e
		}
		if usable {
			state.Paused = false
			state.Reason = ""
			state.ConsecutiveDeclines = 0
			if err = savePaymentControl(v, state); err != nil {
				return 0, err
			}
		}
	}
	return len(blocks), nil
}

// hasUsableCard reports whether any configured card is not permanently
// blocked. Cooling cards count as usable because they return automatically.
func hasUsableCard(v *vault.Vault) (bool, error) {
	cards, err := readCards(v)
	if err != nil {
		return false, nil
	}
	blocks, err := readCardBlocks(v)
	if err != nil {
		return false, err
	}
	for _, c := range cards {
		if validateCard(c) != nil {
			continue
		}
		if block, blocked := blocks[cardFingerprint(c)]; !blocked || (block.Until != 0 && block.Until <= time.Now().Unix()) {
			return true, nil
		}
	}
	return false, nil
}

// CardStatus is a number-free view of one configured card for the CLI and the
// admin dashboard.
type CardStatus struct {
	Last4          string `json:"last4"`
	Usable         bool   `json:"usable"`
	Problem        string `json:"problem,omitempty"`
	Blocked        string `json:"blocked,omitempty"`
	CoolingSeconds int64  `json:"cooling_seconds,omitempty"`
	PairCooling    int    `json:"pair_cooling,omitempty"`
}

func CardsStatus(v *vault.Vault) ([]CardStatus, error) {
	cards, err := readCardsOptional(v)
	if err != nil {
		return nil, err
	}
	blocks, err := readCardBlocks(v)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	status := make([]CardStatus, 0, len(cards))
	for _, c := range cards {
		entry := CardStatus{Last4: cardTail(c), Usable: true}
		fingerprint := cardFingerprint(c)
		if problem := validateCard(c); problem != nil {
			entry.Usable = false
			entry.Problem = problem.Error()
		}
		// Expired cooldowns no longer keep the card out of rotation, so they
		// must not be reported as blocks.
		if block, ok := blocks[fingerprint]; ok && activeCardBlock(block, now) {
			entry.Blocked = block.Reason
			if block.Until > 0 {
				entry.CoolingSeconds = block.Until - now
			}
		}
		for key, block := range blocks {
			if !strings.HasPrefix(key, fingerprint+":") {
				continue
			}
			if block.Until > now {
				entry.PairCooling++
			}
		}
		status = append(status, entry)
	}
	return status, nil
}

// AddCardRecords appends cards from a JSON object or array. Missing billing
// fields are inherited from an existing card because cardholders normally keep
// one billing profile. Duplicate numbers replace the existing entry.
func AddCardRecords(v *vault.Vault, raw []byte) (int, error) {
	incoming, err := decodeCardInput(raw)
	if err != nil {
		return 0, err
	}
	cards, err := readCardsOptional(v)
	if err != nil {
		return 0, err
	}
	return mergeCards(v, cards, incoming, true)
}

// SetCardRecords replaces the whole card set with a validated JSON array.
func SetCardRecords(v *vault.Vault, raw []byte) (int, error) {
	incoming, err := decodeCardInput(raw)
	if err != nil {
		return 0, err
	}
	return mergeCards(v, nil, incoming, false)
}

func decodeCardInput(raw []byte) ([]card, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, errors.New("stdin must contain a card object or a JSON array of cards")
	}
	var many []card
	if strings.HasPrefix(trimmed, "[") {
		if json.Unmarshal(raw, &many) != nil {
			return nil, errors.New("stdin must be a valid JSON array of cards")
		}
	} else {
		var one card
		if json.Unmarshal(raw, &one) != nil {
			return nil, errors.New("stdin must be a valid card object")
		}
		many = []card{one}
	}
	if len(many) == 0 {
		return nil, errors.New("no cards supplied")
	}
	return many, nil
}

func mergeCards(v *vault.Vault, existing, incoming []card, inherit bool) (int, error) {
	base := card{}
	for _, c := range existing {
		if validateCard(c) == nil {
			base = c
			break
		}
	}
	merged := append([]card{}, existing...)
	for _, c := range incoming {
		if inherit {
			c = inheritBilling(c, base)
		}
		if err := validateCard(c); err != nil {
			return 0, fmt.Errorf("card ending %s: %w", cardTail(c), err)
		}
		replaced := false
		for i := range merged {
			if merged[i].Number == c.Number {
				merged[i] = c
				replaced = true
				break
			}
		}
		if !replaced {
			merged = append(merged, c)
		}
	}
	if len(merged) == 0 {
		return 0, errors.New("card set would be empty")
	}
	b, err := json.Marshal(merged)
	if err != nil {
		return 0, err
	}
	defer clear(b)
	if err = v.Put(cardsRecord, b); err != nil {
		return 0, err
	}
	return len(merged), nil
}

func inheritBilling(c card, base card) card {
	for _, field := range []struct {
		value *string
		base  string
	}{
		{&c.Name, base.Name},
		{&c.Email, base.Email},
		{&c.Country, base.Country},
		{&c.Postal, base.Postal},
		{&c.Line1, base.Line1},
		{&c.Line2, base.Line2},
		{&c.City, base.City},
		{&c.State, base.State},
	} {
		if strings.TrimSpace(*field.value) == "" {
			*field.value = field.base
		}
	}
	return c
}

// RemoveCardRecord deletes the card ending in the given last four digits.
func RemoveCardRecord(v *vault.Vault, last4 string) (int, error) {
	if !regexp.MustCompile(`^[0-9]{4}$`).MatchString(last4) {
		return 0, errors.New("--last4 must be exactly four digits")
	}
	cards, err := readCards(v)
	if err != nil {
		return 0, err
	}
	matches := 0
	kept := make([]card, 0, len(cards))
	for _, c := range cards {
		if cardTail(c) == last4 {
			matches++
			continue
		}
		kept = append(kept, c)
	}
	if matches == 0 {
		return 0, fmt.Errorf("no configured card ends with %s", last4)
	}
	if matches > 1 {
		return 0, errors.New("more than one configured card ends with those digits; remove it from the JSON set instead")
	}
	if len(kept) == 0 {
		return 0, errors.New("refusing to remove the last payment card")
	}
	b, err := json.Marshal(kept)
	if err != nil {
		return 0, err
	}
	defer clear(b)
	if err = v.Put(cardsRecord, b); err != nil {
		return 0, err
	}
	return len(kept), nil
}

// UpdateCardBilling applies billing fields to every configured card.
func UpdateCardBilling(v *vault.Vault, fields map[string]string) (int, error) {
	allowed := map[string]bool{"billing_name": true, "email": true, "billing_country": true, "billing_postal_code": true, "billing_address_line1": true, "billing_address_line2": true, "billing_city": true, "billing_state": true}
	for name := range fields {
		if !allowed[name] {
			return 0, errors.New("unsupported billing field")
		}
	}
	cards, err := readCards(v)
	if err != nil {
		return 0, err
	}
	updated := make([]card, 0, len(cards))
	for _, c := range cards {
		raw, err := json.Marshal(c)
		if err != nil {
			return 0, err
		}
		defer clear(raw)
		var record map[string]any
		if json.Unmarshal(raw, &record) != nil {
			return 0, errors.New("invalid card record")
		}
		for name, value := range fields {
			record[name] = value
		}
		out, err := json.Marshal(record)
		if err != nil {
			return 0, err
		}
		defer clear(out)
		var next card
		if json.Unmarshal(out, &next) != nil {
			return 0, errors.New("invalid card record after billing update")
		}
		if err = validateCard(next); err != nil {
			return 0, fmt.Errorf("card ending %s: %w", cardTail(next), err)
		}
		updated = append(updated, next)
	}
	b, err := json.Marshal(updated)
	if err != nil {
		return 0, err
	}
	defer clear(b)
	if _, err = v.Get(cardsRecord); err == nil {
		return len(updated), v.Put(cardsRecord, b)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	return len(updated), v.Put(legacyCardKey, b)
}

// RotationInfo is a number-free view of the running card+node batch for the CLI.
type RotationInfo struct {
	BatchSize int      `json:"batch_size"`
	Used      int      `json:"used"`
	CardLast4 string   `json:"card_last4,omitempty"`
	Node      string   `json:"node,omitempty"`
	Cards     int      `json:"cards"`
	Usable    int      `json:"usable"`
	Blocked   []string `json:"blocked,omitempty"`
	Cooling   []string `json:"cooling,omitempty"`
}

func PaymentRotationStatus(v *vault.Vault) (RotationInfo, error) {
	cards, err := readCardsOptional(v)
	if err != nil {
		return RotationInfo{}, err
	}
	blocks, err := readCardBlocks(v)
	if err != nil {
		return RotationInfo{}, err
	}
	info := RotationInfo{BatchSize: PaymentBatchSize, Cards: len(cards)}
	for _, c := range cards {
		if validateCard(c) == nil {
			info.Usable++
		}
		if block, ok := blocks[cardFingerprint(c)]; ok {
			if block.Until > time.Now().Unix() {
				info.Cooling = append(info.Cooling, fmt.Sprintf("%s (%s)", cardTail(c), block.Reason))
			} else if block.Until == 0 {
				info.Blocked = append(info.Blocked, cardTail(c)+" ("+block.Reason+")")
			}
		}
	}
	rotation, err := readPaymentRotation(v)
	if err != nil {
		return RotationInfo{}, err
	}
	if rotation == nil {
		return info, nil
	}
	info.Used = rotation.Used
	if c, ok := cardByFingerprint(cards, rotation.CardFingerprint); ok {
		info.CardLast4 = cardTail(c)
	}
	if len(rotation.Outbound) > 0 {
		if routeGroup(rotation.Outbound) == "direct" {
			info.Node = "direct"
		} else {
			info.Node = "node-" + rotation.NodeID[:12]
		}
	}
	return info, nil
}
