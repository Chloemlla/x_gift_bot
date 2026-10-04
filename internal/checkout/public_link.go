package checkout

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"xgift/internal/vault"
)

var ErrPublicLinkConflict = errors.New("existing checkout requires private review")

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
	if existing != nil {
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
	persist := func() error {
		b, e := json.Marshal(publicLinkRecord{Owner: ownerHash, Order: r})
		if e != nil {
			return e
		}
		return v.Put("public-checkout:"+recipient, b)
	}
	// Reserve before requesting an external session. An ambiguous failure must
	// never cause an automatic replacement or release the account to auto-pay.
	if err = persist(); err != nil {
		return nil, err
	}
	r.SessionID, r.URL, err = x.create(ctx, user, recipient, plan)
	if err != nil {
		return nil, err
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
			return nil, ErrPublicLinkConflict
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
	if saved.Owner != owner || r.Username != user || r.RecipientID != recipient || r.Months != plan.Months || r.Amount != plan.Minor || r.Currency != strings.ToUpper(plan.Currency) || r.ProductID != plan.ProductID || r.Status != "created" || !unsubmitted(&r) || r.CardFingerprint != "" || CheckoutLink(&r) == "" {
		return nil, ErrPublicLinkConflict
	}
	return &r, nil
}
