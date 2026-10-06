package checkout

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"xgift/internal/vault"
)

var ErrPublicLinkConflict = errors.New("existing checkout requires private review")
var ErrPublicLinkPrivateOrder = fmt.Errorf("%w: private order exists", ErrPublicLinkConflict)
var ErrPublicLinkPending = errors.New("public checkout creation returned no usable link")
var ErrPublicPaymentInProgress = errors.New("public checkout payment is in progress")

const publicLinkTTL = 15 * time.Minute

type publicLinkRecord struct {
	Owner string `json:"owner"`
	Order Record `json:"order"`
}

// PublicLinkForUsername never uses a saved card or an automatic-payment record.
// Caller must hold checkout.lock. The browser cookie identifies queue requests;
// verified public links may be retrieved across browsers for the same recipient.
func PublicLinkForUsername(ctx context.Context, v *vault.Vault, user, owner string, port, months int, verifiedUnpaid ...bool) (*Record, error) {
	user = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(user), "@"))
	if !regexp.MustCompile(`^[a-z0-9_]{1,15}$`).MatchString(user) || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(owner) {
		return nil, errors.New("invalid public link request")
	}
	cat, err := ReadCatalog(v)
	if err != nil {
		return nil, err
	}
	plan, err := cat.PlanFor(months)
	if err != nil {
		return nil, err
	}
	x, err := newXClient(v, port)
	if err != nil {
		return nil, err
	}
	defer x.close()
	return publicLinkForClient(ctx, v, user, owner, plan, x, verifiedUnpaid...)
}

func publicLinkForClient(ctx context.Context, v *vault.Vault, user, owner string, plan Plan, x *xClient, verifiedUnpaid ...bool) (*Record, error) {
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
		if err != nil && !errors.Is(err, ErrVerifyUnpaid) {
			return nil, err
		}
		if err == nil && publicLinkMatches(existing, plan) && publicLinkFresh(existing, time.Now()) {
			return existing, holdPublicCheckout(v, existing, plan, time.Now())
		}
		replace = true
		x.publicReplacement = existing.SessionID
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
	// One attempt per explicit request. Older unpublished reservations predate
	// CreationAttempts, so count their first upstream attempt conservatively.
	if existing != nil && !replace && r.CreationAttempts == 0 && !r.CreationRetryable {
		r.CreationAttempts = 1
	}
	r.CreationAttempts++
	r.CreationRetryable = true
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
			Previous       json.RawMessage `json:"previous"`
			VerifiedUnpaid bool            `json:"verified_unpaid"`
			VerifiedAt     int64           `json:"verified_at"`
		}{old, len(verifiedUnpaid) > 0 && verifiedUnpaid[0], time.Now().Unix()})
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
			// No upstream attempt occurred. A queue wait cannot consume retries.
			r.CreationAttempts--
			if e := persist(); e != nil {
				return nil, e
			}
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
		if (p.Intent.Status == "requires_payment_method" || p.Intent.Status == "canceled") && p.Intent.AmountReceived != nil && *p.Intent.AmountReceived == 0 {
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
	user = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(user), "@"))
	if !regexp.MustCompile(`^[a-z0-9_]{1,15}$`).MatchString(user) || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(owner) {
		return nil, false, errors.New("invalid public link request")
	}
	cat, err := ReadCatalog(v)
	if err != nil {
		return nil, false, err
	}
	plan, err := cat.PlanFor(months)
	if err != nil {
		return nil, false, err
	}
	x, err := newXClient(v, port)
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
