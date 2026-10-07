package checkout

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"xgift/internal/vault"
)

// PublicOrderStatus returns the newest public order for user that was created
// by the browser owning this cookie, or nil. It reads local records only.
func PublicOrderStatus(v *vault.Vault, user, owner string) (*Record, error) {
	sum := sha256.Sum256([]byte(owner))
	ownerHash := hex.EncodeToString(sum[:])
	names, err := v.NamesWithPrefix("public-checkout:")
	if err != nil {
		return nil, err
	}
	var found *Record
	for _, name := range names {
		b, err := v.Get(name)
		if err != nil {
			return nil, err
		}
		var saved publicLinkRecord
		err = json.Unmarshal(b, &saved)
		clear(b)
		if err != nil || saved.Owner != ownerHash || saved.Order.Username != user {
			continue
		}
		if found == nil || saved.Order.Created > found.Created {
			r := saved.Order
			found = &r
		}
	}
	if found == nil || found.Status != "created" {
		return found, nil
	}
	// The queue's payment-window check records verified payments here before
	// the next link is created; use it when the order record was not updated.
	a, err := readActiveCheckout(v)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil && a.Order.SessionID == found.SessionID && a.Order.Status == "succeeded" {
		found.Status = "succeeded"
	}
	return found, nil
}

// PublicOrderOpen reports whether a created public order is inside its window.
func PublicOrderOpen(r *Record, now time.Time) bool {
	return r.Status == "created" && publicLinkFresh(r, now)
}

// PublicOrderPaid asks Stripe, read-only, whether a created public order was
// paid. It never creates, submits or changes a checkout session.
func PublicOrderPaid(ctx context.Context, v *vault.Vault, r *Record) (bool, error) {
	cat, err := ReadCatalog(v)
	if err != nil {
		return false, err
	}
	plan := Plan{Months: r.Months, Minor: r.Amount, Currency: strings.ToLower(r.Currency), ProductID: r.ProductID, Merchant: cat.Merchant}
	return verifiedCheckoutPaid(ctx, v, r, plan)
}

// RecordPublicOrderPaid stores a Stripe-confirmed payment on the public order
// and frees its payment window. Caller holds checkout.lock.
func RecordPublicOrderPaid(v *vault.Vault, r *Record) error {
	key := "public-checkout:" + r.RecipientID
	b, err := v.Get(key)
	if err != nil {
		return err
	}
	defer clear(b)
	var saved publicLinkRecord
	if err = json.Unmarshal(b, &saved); err != nil {
		return err
	}
	if saved.Order.SessionID != r.SessionID || saved.Order.Status != "created" {
		return nil // replaced or already updated
	}
	saved.Order.Status = "succeeded"
	next, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	if err = v.Put(key, next); err != nil {
		return err
	}
	a, err := currentActiveCheckout(v)
	if err != nil || a.Released || a.Order.SessionID != r.SessionID {
		return err
	}
	a.Released, a.Order.Status = true, "succeeded"
	return saveActiveCheckout(v, a)
}
