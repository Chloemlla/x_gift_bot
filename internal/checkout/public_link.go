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
var ErrPublicLinkOtherBrowser = fmt.Errorf("%w: browser ownership mismatch", ErrPublicLinkConflict)
var ErrPublicLinkPlan = fmt.Errorf("%w: another plan exists", ErrPublicLinkConflict)
var ErrPublicLinkRetryLimit = fmt.Errorf("%w: unpublished creation retry budget exhausted", ErrPublicLinkConflict)
var ErrPublicLinkPending = errors.New("public checkout creation returned no usable link")

type publicLinkRecord struct {
	Owner string `json:"owner"`
	Order Record `json:"order"`
}

// PublicLinkForUsername never uses a saved card or an automatic-payment record.
// Caller must hold checkout.lock. Ownership is an opaque, browser-bound cookie.
func PublicLinkForUsername(ctx context.Context, v *vault.Vault, user, owner string, port, months int) (*Record, error) {
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
	return publicLinkForClient(ctx, v, user, owner, plan, x)
}

func publicLinkForClient(ctx context.Context, v *vault.Vault, user, owner string, plan Plan, x *xClient) (*Record, error) {
	months := plan.Months
	recipient, err := x.identity(ctx, user, false)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(owner))
	ownerHash := hex.EncodeToString(sum[:])
	existing, err := publicLinkExisting(v, user, recipient, ownerHash, plan)
	if err != nil {
		return nil, err
	}
	if existing != nil && existing.Status == "created" {
		return existing, nil
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
	if existing != nil {
		r = *existing
	}
	// One attempt per explicit request. Older unpublished reservations predate
	// CreationAttempts, so count their first upstream attempt conservatively.
	if existing != nil && r.CreationAttempts == 0 {
		r.CreationAttempts = 1
	}
	r.CreationAttempts++
	persist := func() error {
		b, e := json.Marshal(publicLinkRecord{Owner: ownerHash, Order: r})
		if e != nil {
			return e
		}
		return v.Put("public-checkout:"+recipient, b)
	}
	// Reserve before sending. Only an unpublished, card-free creation may be
	// retried on an explicit request; never replace a URL returned to a visitor
	// or release this account to the saved-card payment flow.
	if err = persist(); err != nil {
		return nil, err
	}
	r.SessionID, r.URL, err = x.create(ctx, user, recipient, plan)
	if err != nil {
		return nil, ErrPublicLinkPending
	}
	r.Status = "created"
	if err = persist(); err != nil {
		return nil, err
	}
	return &r, nil
}

func publicLinkExisting(v *vault.Vault, user, recipient, owner string, plan Plan) (*Record, error) {
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
	if saved.Owner != owner {
		return nil, ErrPublicLinkOtherBrowser
	}
	if r.Username != user || r.RecipientID != recipient || !unsubmitted(&r) || r.CardFingerprint != "" {
		return nil, ErrPublicLinkConflict
	}
	if r.Months != plan.Months {
		return nil, ErrPublicLinkPlan
	}
	if r.Amount != plan.Minor || r.Currency != strings.ToUpper(plan.Currency) || r.ProductID != plan.ProductID {
		return nil, ErrPublicLinkConflict
	}
	switch r.Status {
	case "created":
		if CheckoutLink(&r) == "" {
			return nil, ErrPublicLinkConflict
		}
	case "creating":
		if r.URL != "" || r.SessionID != "" || r.PreviousSession != "" || r.ReplacementCount != 0 || r.RecoveryAttempts != 0 || r.ManualRecovery || r.LastError != nil {
			return nil, ErrPublicLinkConflict
		}
		if r.CreationAttempts >= maxAttempts {
			return nil, ErrPublicLinkRetryLimit
		}
	default:
		return nil, ErrPublicLinkConflict
	}
	return &r, nil
}
