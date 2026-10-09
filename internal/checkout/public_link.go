package checkout

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"xgift/internal/vault"
)

var ErrPublicPersistence = errors.New("public checkout persistence failed")

var ErrPublicLinkConflict = errors.New("existing checkout requires private review")
var ErrPublicLinkPrivateOrder = fmt.Errorf("%w: private order exists", ErrPublicLinkConflict)
var ErrPublicLinkPending = errors.New("public checkout creation returned no usable link")
var ErrPublicPaymentDeclined = errors.New("public payment declined; rejoin queue")
var ErrPublicPaymentInProgress = errors.New("public checkout payment is in progress")

// PublicLinkTTL is the local queue window, not upstream session expiration.
const PublicLinkTTL = 3 * time.Minute

const publicLinkTTL = PublicLinkTTL

type publicLinkRecord struct {
	Owner     string   `json:"owner"`
	Order     Record   `json:"order"`
	History   []Record `json:"history,omitempty"`
	RequestID string   `json:"request_id,omitempty"`
}

// PublicLinkForUsername never uses a saved card or an automatic-payment record.
// Caller must hold checkout.lock. Regeneration is scoped to the owning browser.
func PublicLinkForUsername(ctx context.Context, v *vault.Vault, user, owner string, port, months int) (*Record, error) {
	user, plan, x, err := publicSetup(v, user, owner, port, months)
	if err != nil {
		return nil, err
	}
	defer x.close()
	return publicLinkForClient(ctx, v, user, owner, plan, x)
}

// PublicLinkForRequest binds retries to one browser-owned generation request.
// Caller holds checkout.lock. A different request explicitly creates a fresh link.
func PublicLinkForRequest(ctx context.Context, v *vault.Vault, user, owner string, port, months int, requestID string) (*Record, error) {
	user, plan, x, err := publicSetup(v, user, owner, port, months)
	if err != nil {
		return nil, err
	}
	defer x.close()
	x.publicRequestID = requestID
	return publicLinkForClient(ctx, v, user, owner, plan, x)
}

// publicSetup validates a public request and opens the X client for its plan.
func publicSetup(v *vault.Vault, user, owner string, port, months int) (string, Plan, *xClient, error) {
	user, ok := NormalizeUsername(user)
	if !ok || !ValidOwner(owner) {
		return "", Plan{}, nil, errors.New("invalid public link request")
	}
	cat, err := ReadCatalog(v)
	if err != nil {
		return "", Plan{}, nil, err
	}
	plan, err := cat.PlanFor(months)
	if err != nil {
		return "", Plan{}, nil, err
	}
	x, err := newXClient(v, port)
	return user, plan, x, err
}

func publicLinkForClient(ctx context.Context, v *vault.Vault, user, owner string, plan Plan, x *xClient) (*Record, error) {
	months := plan.Months
	x.publicGeneration = true
	x.publicReplacement = ""
	defer func() { x.publicReplacement = "" }()
	recipient, err := x.identity(ctx, user, false)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(owner))
	ownerHash := hex.EncodeToString(sum[:])
	existing, err := publicLinkExisting(v, user, recipient, plan)
	if err != nil {
		return nil, err
	}
	var saved publicLinkRecord
	if existing != nil {
		raw, e := v.Get("public-checkout:" + recipient)
		if e != nil {
			return nil, e
		}
		e = json.Unmarshal(raw, &saved)
		clear(raw)
		if e != nil {
			return nil, e
		}
		// A different browser adopts the order chain: it never receives the old
		// sessions' links, only their payment evidence, then generates its own.
		orders := append(append([]Record{}, saved.History...), *existing)
		for i := range orders {
			old := &orders[i]
			if old.Status == "succeeded" {
				// A settled order only proves its session was paid once; inside its
				// own payment window the result still answers a fresh request, but a
				// long-settled record must not veto every later purchase.
				if publicLinkFresh(old, time.Now()) {
					return old, nil
				}
				continue
			}
			if old.SessionID == "" {
				continue
			}
			oldPlan := Plan{Months: old.Months, Minor: old.Amount, Currency: strings.ToLower(old.Currency), ProductID: old.ProductID, Merchant: plan.Merchant}
			// An unavailable read remains unknown. It does not prevent explicit link
			// creation and is never recorded as proof that the old session is unpaid.
			if e := verifyPublicCheckout(ctx, v, x, old, oldPlan); errors.Is(e, ErrPublicPersistence) {
				return nil, e
			}
			// A payment newly confirmed during this scan still wins: the requester
			// must not be charged again for an order that just settled.
			if old.Status == "succeeded" {
				return old, nil
			}
		}
		raw, e = v.Get("public-checkout:" + recipient)
		if e != nil {
			return nil, e
		}
		e = json.Unmarshal(raw, &saved)
		clear(raw)
		if e != nil {
			return nil, e
		}
		*existing = saved.Order
		if saved.Owner == ownerHash && x.publicRequestID != "" && saved.RequestID == x.publicRequestID {
			if !publicLinkMatches(existing, plan) {
				return nil, ErrPublicLinkConflict
			}
			if existing.Status == "creating" {
				return existing, ErrPublicLinkPending
			}
			// Retry the same request without another upstream mutation.
			if existing.LinkBlocked {
				return nil, ErrPublicLinkPending
			}
			return existing, nil
		}
		for _, old := range saved.History {
			if x.publicRequestID != "" && old.GenerationRequest == x.publicRequestID {
				return nil, ErrPublicLinkPending
			}
		}
		x.publicReplacement = existing.SessionID
		if x.publicReplacement == "" {
			for _, old := range saved.History {
				if old.SessionID != "" {
					x.publicReplacement = old.SessionID
				}
			}
		}
		saved.History = append(saved.History, *existing)
	}
	if err = x.checkCreation(ctx, time.Now()); err != nil {
		return nil, err
	}
	checked, err := x.recipient(ctx, user)
	if err != nil {
		return nil, err
	}
	if checked != recipient {
		return nil, ErrPublicLinkConflict
	}
	if err = x.quote(ctx, user, plan); err != nil {
		return nil, err
	}
	r := Record{GenerationRequest: x.publicRequestID, Username: user, RecipientID: recipient, Months: months, Amount: plan.Minor, Currency: strings.ToUpper(plan.Currency), ProductID: plan.ProductID, Status: "creating", Created: time.Now().Unix()}
	persist := func() error {
		b, e := json.Marshal(publicLinkRecord{Owner: ownerHash, Order: r, History: saved.History, RequestID: x.publicRequestID})
		if e != nil {
			return e
		}
		return v.Put("public-checkout:"+recipient, b)
	}
	if err = persist(); err != nil {
		return nil, err
	}
	r.SessionID, r.URL, err = x.create(ctx, user, recipient, plan)
	if err != nil {
		if errors.Is(err, ErrCheckoutRateLimited) {
			return nil, err
		}
		return nil, ErrPublicLinkPending
	}
	if publicSessionRetained(saved.History, r.SessionID) {
		// Never publish an upstream replay as a newly generated session.
		r.LinkBlocked = true
		r.SessionID, r.URL = "", ""
		if err = persist(); err != nil {
			return nil, err
		}
		return nil, ErrPublicLinkPending
	}

	r.Status = "created"
	r.Created = time.Now().Unix()
	if err = persist(); err != nil {
		return nil, err
	}
	if err = verifyPublicCheckout(ctx, v, x, &r, plan); err != nil {
		return nil, err
	}
	if err = persist(); err != nil {
		return nil, err
	}
	if err = x.releasePublicReplacement(v); err != nil {
		return nil, err
	}
	if err = holdPublicCheckout(v, &r, plan, time.Now()); err != nil {
		return nil, err
	}
	// Give the visitor the full interval after verification, even when the X or
	// Stripe request was slow. All creation paths honor this persisted clock.
	stamp, _ := json.Marshal(time.Now().UnixMilli())
	if err = v.Put("checkout-creation:last", stamp); err != nil {
		return nil, err
	}
	return &r, nil
}

// Verify against Stripe, not merely X's "Unpaid" response or a cached URL.
// No card tokenization or payment confirmation occurs in this path.
func verifyPublicCheckout(ctx context.Context, v *vault.Vault, x *xClient, r *Record, plan Plan) (resultErr error) {
	defer func() {
		r.LinkBlocked = resultErr != nil || r.Status != "created"
		if err := persistPublicVerification(v, r); err != nil {
			resultErr = ErrPublicPersistence
		}
	}()
	read := x.readCheckout
	if read == nil {
		read = func(ctx context.Context, r *Record) (*paymentPage, error) {
			s, err := newStripe(ctx, v, r.RecipientID, paymentRead)
			if err != nil {
				return nil, err
			}
			defer s.close()
			return s.page(ctx, r, true)
		}
	}
	p, err := read(ctx, r)
	if inactiveCheckout(err) {
		if paid, _ := x.checkoutPaid(ctx, r, plan); paid {
			r.Status = "succeeded"
			return nil
		}
		return ErrPublicPaymentInProgress
	}
	if err != nil {
		return err
	}
	if err = p.guard(r, plan, false); err != nil {
		return err
	}
	if err = rememberVerifiedCheckout(v, r, plan, p); err != nil {
		return err
	}
	if p.Status == "complete" && p.PaymentStatus == "paid" {
		r.Status = "succeeded"
		return nil
	}
	if p.Status == "expired" && p.PaymentStatus == "unpaid" && p.IntentPresent && p.IntentNull && p.Intent == nil {
		return ErrVerifyUnpaid
	}
	// Public links never submit a saved card. A failed/manual payment may
	// leave an intent waiting for a new payment method; that is not an active
	// payment and must not permanently prevent changing the gift duration.
	if p.Intent != nil {
		if publicIntentDeclined(p) {
			return ErrPublicPaymentDeclined
		}
		if publicIntentIdle(p) {
			if p.Status == "expired" && p.PaymentStatus == "unpaid" {
				return ErrVerifyUnpaid
			}
			manual := *p
			manual.Intent, manual.IntentNull, manual.IntentPresent = nil, true, true
			return manual.guard(r, plan, true)
		}
		return ErrPublicPaymentInProgress
	}
	return p.guard(r, plan, true)
}

// Only explicit refusal evidence can end an otherwise valid payment window.
// An unused intent also requires_payment_method, so that status alone is insufficient.
func publicIntentDeclined(p *paymentPage) bool {
	if !publicIntentIdle(p) {
		return false
	}
	var extra struct {
		Intent struct {
			Error struct {
				Code        string `json:"code"`
				DeclineCode string `json:"decline_code"`
			} `json:"last_payment_error"`
		} `json:"payment_intent"`
	}
	if json.Unmarshal(p.raw, &extra) != nil {
		return false
	}
	return extra.Intent.Error.Code == "card_declined" || extra.Intent.Error.DeclineCode != ""
}

// Stripe's publishable-key responses omit amount_received/capturable. The live,
// guarded requires_payment_method/canceled status proves no payment is in flight;
// missing private fields are not evidence of processing. Reject contradictory
// explicit funds evidence. This never confirms a card or reports payment success.
func publicIntentIdle(p *paymentPage) bool {
	if p.Intent == nil || p.PaymentStatus != "unpaid" || (p.Intent.Status != "requires_payment_method" && p.Intent.Status != "canceled") || (p.Intent.AmountReceived != nil && *p.Intent.AmountReceived != 0) {
		return false
	}
	if len(p.raw) > 0 {
		var extra struct {
			Intent struct {
				Capturable *int `json:"amount_capturable"`
			} `json:"payment_intent"`
		}
		if json.Unmarshal(p.raw, &extra) != nil || (extra.Intent.Capturable != nil && *extra.Intent.Capturable != 0) {
			return false
		}
	}
	return true
}

func publicLinkExisting(v *vault.Vault, user, recipient string, plan Plan) (*Record, error) {
	for _, key := range []string{"checkout:" + recipient, "checkout:" + user} {
		b, e := v.Get(key)
		if errors.Is(e, sql.ErrNoRows) {
			continue
		}
		if e != nil {
			return nil, e
		}
		var r Record
		inFlight := json.Unmarshal(b, &r) == nil && privateOrderInFlight(&r)
		clear(b)
		if inFlight {
			return nil, ErrPublicLinkPrivateOrder
		}
	}
	b, e := v.Get("public-checkout:" + recipient)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	defer clear(b)
	var saved publicLinkRecord
	if json.Unmarshal(b, &saved) == nil && (!unsubmitted(&saved.Order) || saved.Order.CardFingerprint != "") {
		// Public orders are never submitted by us; payment evidence in this
		// keyspace is contamination and stays operator-visible.
		return nil, ErrPublicLinkConflict
	}
	var r Record
	usable := false
	if json.Unmarshal(b, &saved) == nil {
		r = saved.Order
		usable = publicRecordUsable(&r, recipient)
	}
	if !usable {
		// Renamed, stale or damaged records used to brick the account until an
		// operator intervened. Archive and continue as if none existed.
		if err := archiveBrokenPublicCheckout(v, recipient, b); err != nil {
			return nil, err
		}
		return nil, nil
	}
	return &r, nil
}

// privateOrderInFlight reports a card payment submitted by us whose outcome is
// still open; that is the only private order that vetoes a new public link.
func privateOrderInFlight(r *Record) bool {
	return !unsubmitted(r) && !IsPaymentDeclined(r) && r.Status != "succeeded"
}

// publicRecordUsable validates a stored public order's identity and shape. The
// recipient key is authoritative: a renamed account still owns its history.
func publicRecordUsable(r *Record, recipient string) bool {
	if r.RecipientID != recipient {
		return false
	}
	if r.Months < 1 || r.Months > 24 || r.Amount <= 0 || !catalogCurrencyPattern.MatchString(strings.ToLower(r.Currency)) || !catalogProductPattern.MatchString(r.ProductID) {
		return false
	}
	switch r.Status {
	case "created", "succeeded":
		return sessionURL(r.URL, r.SessionID)
	case "creating":
		if (r.SessionID != "" || r.URL != "") && !sessionURL(r.URL, r.SessionID) {
			return false
		}
		// Preserve an uncertain creation reservation and its original product.
		return r.PreviousSession == "" && r.ReplacementCount == 0 && r.RecoveryAttempts == 0 && !r.ManualRecovery && r.LastError == nil
	}
	return false
}

// archiveBrokenPublicCheckout preserves an unusable stored order under a
// history key and clears it, so the account can get a fresh link.
func archiveBrokenPublicCheckout(v *vault.Vault, recipient string, raw []byte) error {
	proof, _ := json.Marshal(struct {
		Previous   json.RawMessage `json:"previous"`
		ArchivedAt int64           `json:"archived_at"`
		Reason     string          `json:"reason"`
	}{json.RawMessage(append([]byte(nil), raw...)), time.Now().Unix(), "unusable_record"})
	return v.Archive("public-checkout:"+recipient, fmt.Sprintf("public-checkout-history:%s:%d", recipient, time.Now().UnixNano()), raw, proof)
}

// publicSessionPayable reports whether the stored public order for a recipient
// still contains a session that could take a browser payment. Stripe expires
// open checkout sessions after 24 hours; an old or settled record is inert for
// the card flow.
func publicSessionPayable(v *vault.Vault, recipient string, now time.Time) (bool, error) {
	raw, err := v.Get("public-checkout:" + recipient)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer clear(raw)
	var saved publicLinkRecord
	if json.Unmarshal(raw, &saved) != nil {
		return false, nil
	}
	orders := append(append([]Record{}, saved.History...), saved.Order)
	for i := range orders {
		o := &orders[i]
		if o.SessionID != "" && o.Status != "succeeded" && o.Created > 0 && now.Sub(time.Unix(o.Created, 0)) < 24*time.Hour {
			return true, nil
		}
	}
	return false, nil
}

func publicLinkMatches(r *Record, plan Plan) bool {
	return r.Months == plan.Months && r.Amount == plan.Minor && r.Currency == strings.ToUpper(plan.Currency) && r.ProductID == plan.ProductID
}

func publicLinkFresh(r *Record, now time.Time) bool {
	created := time.Unix(r.Created, 0)
	return r.Created > 0 && !now.Before(created) && now.Sub(created) < publicLinkTTL
}

// Verification updates the same session only, preserving browser ownership.
// Callers hold checkout.lock; never overwrite a newer checkout with an old read.
func persistPublicVerification(v *vault.Vault, r *Record) error {
	key := "public-checkout:" + r.RecipientID
	raw, err := v.Get(key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	defer clear(raw)
	var saved publicLinkRecord
	if err = json.Unmarshal(raw, &saved); err != nil {
		return err
	}
	if saved.Order.SessionID == r.SessionID {
		if saved.Order.Status == "succeeded" && r.Status != "succeeded" {
			return nil
		}
		saved.Order = *r
	} else {
		found := false
		for i := range saved.History {
			if saved.History[i].SessionID == r.SessionID {
				if saved.History[i].Status == "succeeded" && r.Status != "succeeded" {
					return nil
				}
				saved.History[i] = *r
				found = true
				break
			}
		}
		if !found {
			return ErrPublicLinkConflict
		}
	}
	b, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	return v.Put(key, b)
}

// VerifyExistingPublicLink only queries the exact persisted public session.
// Caller holds checkout.lock. No X creation request or card API can run here.
func VerifyExistingPublicLink(ctx context.Context, v *vault.Vault, r *Record) error {
	cat, err := ReadCatalog(v)
	if err != nil {
		return err
	}
	plan := Plan{Months: r.Months, Minor: r.Amount, Currency: strings.ToLower(r.Currency), ProductID: r.ProductID, Merchant: cat.Merchant}
	paid, err := PublicOrderPaid(ctx, v, r)
	if paid {
		return RecordPublicOrderPaid(v, r)
	}
	_ = err // unknown history remains unknown; this path verifies only current link

	x := &xClient{vault: v}
	x.readCheckout = func(ctx context.Context, r *Record) (*paymentPage, error) {
		s, err := newStripe(ctx, v, r.RecipientID, paymentRead)
		if err != nil {
			return nil, err
		}
		defer s.close()
		return s.page(ctx, r, false)
	}
	x.readCheckoutPaid = func(ctx context.Context, r *Record, plan Plan) (bool, error) {
		return verifiedCheckoutPaid(ctx, v, r, plan)
	}
	return verifyPublicCheckout(ctx, v, x, r, plan)
}

func publicSessionRetained(history []Record, session string) bool {
	for _, r := range history {
		if r.SessionID != "" && r.SessionID == session {
			return true
		}
	}
	return false
}
