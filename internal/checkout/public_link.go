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

var ErrPublicLinkConflict = errors.New("existing checkout requires private review")
var ErrPublicLinkPrivateOrder = fmt.Errorf("%w: private order exists", ErrPublicLinkConflict)
var ErrPublicLinkPending = errors.New("public checkout creation returned no usable link")
var ErrPublicPaymentDeclined = errors.New("public payment declined; rejoin queue")
var ErrPublicPaymentInProgress = errors.New("public checkout payment is in progress")

// PublicLinkTTL is the fixed payment window, measured from order creation.
const PublicLinkTTL = 3 * time.Minute

const publicLinkTTL = PublicLinkTTL

type publicLinkRecord struct {
	Owner string `json:"owner"`
	Order Record `json:"order"`
}

// PublicLinkForUsername never uses a saved card or an automatic-payment record.
// Caller must hold checkout.lock. The browser cookie identifies queue requests;
// verified public links may be retrieved across browsers for the same recipient.
func PublicLinkForUsername(ctx context.Context, v *vault.Vault, user, owner string, port, months int) (*Record, error) {
	user, plan, x, err := publicSetup(v, user, owner, port, months)
	if err != nil {
		return nil, err
	}
	defer x.close()
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
	if existing != nil && existing.Status == "succeeded" {
		return existing, nil
	}
	replace := false
	if existing != nil && existing.Status == "created" {
		// Validate the old session against its original product, even when the
		// visitor selected a different plan. Never return that link for a new plan.
		oldPlan := Plan{Months: existing.Months, Minor: existing.Amount, Currency: strings.ToLower(existing.Currency), ProductID: existing.ProductID, Merchant: plan.Merchant}
		err = verifyPublicCheckout(ctx, v, x, existing, oldPlan)
		if err == nil && existing.Status == "succeeded" {
			b, _ := json.Marshal(publicLinkRecord{Owner: ownerHash, Order: *existing})
			return existing, v.Put("public-checkout:"+recipient, b)
		}
		declined := errors.Is(err, ErrPublicPaymentDeclined)
		if declined {
			released, releaseErr := releaseDeclinedCheckout(v, existing.SessionID)
			if releaseErr != nil {
				return nil, releaseErr
			}
			if released {
				return nil, ErrPublicPaymentDeclined
			}
		}
		if err != nil && !errors.Is(err, ErrVerifyUnpaid) && !declined {
			return nil, err
		}
		if err == nil && publicLinkMatches(existing, plan) && publicLinkFresh(existing, time.Now()) {
			return existing, holdPublicCheckout(v, existing, plan, time.Now())
		}
		replace = true
		if !declined {
			x.publicReplacement = existing.SessionID
		}
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
	r := Record{Username: user, RecipientID: recipient, Months: months, Amount: plan.Minor, Currency: strings.ToUpper(plan.Currency), ProductID: plan.ProductID, Status: "creating", Created: time.Now().Unix()}
	if existing != nil && !replace {
		r = *existing
	}
	persist := func() error {
		b, e := json.Marshal(publicLinkRecord{Owner: ownerHash, Order: r})
		if e != nil {
			return e
		}
		return v.Put("public-checkout:"+recipient, b)
	}
	// Preserve previous sessions without interpreting inactivity as payment failure.
	// The public reservation remains isolated from saved-card payment records.
	if replace {
		old, e := v.Get("public-checkout:" + recipient)
		if e != nil {
			return nil, e
		}
		proof, _ := json.Marshal(struct {
			Previous   json.RawMessage `json:"previous"`
			VerifiedAt int64           `json:"verified_at"`
		}{old, time.Now().Unix()})
		next, _ := json.Marshal(publicLinkRecord{Owner: ownerHash, Order: r})
		err = v.ReplaceArchived("public-checkout:"+recipient, fmt.Sprintf("public-checkout-history:%s:%d", recipient, time.Now().UnixNano()), old, proof, next)
	} else {
		err = persist()
	}
	if err != nil {
		return nil, err
	}
	// The user requested a replacement for this public order. Retire only its
	// own reservation; a different recipient's payment window remains protected.
	if replace {
		if err = x.releasePublicReplacement(v); err != nil {
			return nil, err
		}
	}
	r.SessionID, r.URL, err = x.create(ctx, user, recipient, plan)
	if err != nil {
		if errors.Is(err, ErrCheckoutRateLimited) {
			return nil, err
		}
		return nil, ErrPublicLinkPending
	}
	if existing != nil && existing.SessionID == r.SessionID {
		// An upstream replay cannot reset the old session's TTL or become a
		// newly published link on a later request.
		r = *existing
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
func verifyPublicCheckout(ctx context.Context, v *vault.Vault, x *xClient, r *Record, plan Plan) error {
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
		return ErrVerifyUnpaid
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
		clear(b)
		if e == nil {
			return nil, ErrPublicLinkPrivateOrder
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return nil, e
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
	if json.Unmarshal(b, &saved) != nil {
		return nil, ErrPublicLinkConflict
	}
	r := saved.Order
	if r.Username != user || r.RecipientID != recipient || !unsubmitted(&r) || r.CardFingerprint != "" {
		return nil, ErrPublicLinkConflict
	}
	if r.Months < 1 || r.Months > 24 || r.Amount <= 0 || !catalogCurrencyPattern.MatchString(strings.ToLower(r.Currency)) || !catalogProductPattern.MatchString(r.ProductID) {
		return nil, ErrPublicLinkConflict
	}
	switch r.Status {
	case "created", "succeeded":
		if CheckoutLink(&r) == "" {
			return nil, ErrPublicLinkConflict
		}
	case "creating":
		// An unpublished request has no payable session to verify. Keep its
		// retry budget, but allow the current request to choose the product.
		r.Months, r.Amount, r.Currency, r.ProductID = plan.Months, plan.Minor, strings.ToUpper(plan.Currency), plan.ProductID
		if r.URL != "" || r.SessionID != "" || r.PreviousSession != "" || r.ReplacementCount != 0 || r.RecoveryAttempts != 0 || r.ManualRecovery || r.LastError != nil {
			return nil, ErrPublicLinkConflict
		}
	default:
		return nil, ErrPublicLinkConflict
	}
	return &r, nil
}

func publicLinkMatches(r *Record, plan Plan) bool {
	return r.Months == plan.Months && r.Amount == plan.Minor && r.Currency == strings.ToUpper(plan.Currency) && r.ProductID == plan.ProductID
}

func publicLinkFresh(r *Record, now time.Time) bool {
	created := time.Unix(r.Created, 0)
	return r.Created > 0 && !now.Before(created) && now.Sub(created) < publicLinkTTL
}

// TryCachedPublicLink is a non-creating bypass for the holder of the active
// payment window. A cache miss must join the normal queue, never create here.
// Caller holds checkout.lock across the entire operation.
func TryCachedPublicLink(ctx context.Context, v *vault.Vault, user, owner string, port, months int) (*Record, bool, error) {
	user, plan, x, err := publicSetup(v, user, owner, port, months)
	if err != nil {
		return nil, false, err
	}
	defer x.close()
	return cachedPublicLinkForClient(ctx, v, user, owner, plan, x)
}
func cachedPublicLinkForClient(ctx context.Context, v *vault.Vault, user, owner string, plan Plan, x *xClient) (*Record, bool, error) {
	recipient, err := x.identity(ctx, user, false)
	if err != nil {
		return nil, false, err
	}
	r, err := publicLinkExisting(v, user, recipient, plan)
	if err != nil {
		return nil, false, err
	}
	if r == nil || r.Status != "created" || !publicLinkFresh(r, time.Now()) || !publicLinkMatches(r, plan) {
		return nil, false, nil
	}
	if err = verifyPublicCheckout(ctx, v, x, r, plan); err != nil {
		if errors.Is(err, ErrPublicPaymentDeclined) {
			if _, releaseErr := releaseDeclinedCheckout(v, r.SessionID); releaseErr != nil {
				return nil, true, releaseErr
			}
		}
		return nil, true, err
	}
	if r.Status == "created" && !publicLinkFresh(r, time.Now()) {
		return nil, false, nil
	}
	if r.Status == "succeeded" {
		sum := sha256.Sum256([]byte(owner))
		b, _ := json.Marshal(publicLinkRecord{Owner: hex.EncodeToString(sum[:]), Order: *r})
		err = v.Put("public-checkout:"+recipient, b)
	} else {
		err = holdPublicCheckout(v, r, plan, time.Now())
	}
	return r, true, err
}
