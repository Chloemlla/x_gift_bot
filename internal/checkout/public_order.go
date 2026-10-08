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
			for _, old := range saved.History {
				if old.Status == "succeeded" {
					r = old
					break
				}
			}
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
	raw, err := v.Get("public-checkout:" + r.RecipientID)
	if err != nil {
		return false, err
	}
	defer clear(raw)
	var saved publicLinkRecord
	if err = json.Unmarshal(raw, &saved); err != nil {
		return false, err
	}
	// Read each retained session using its original amount/product. A paid old
	// session is reported with its own identity, never copied onto the new one.
	orders := append(append([]Record{}, saved.History...), saved.Order)
	var unknown error
	for _, old := range orders {
		if old.Status == "succeeded" {
			*r = old
			return true, nil
		}
		if old.SessionID == "" {
			unknown = ErrPublicLinkPending
			continue
		}
		plan := Plan{Months: old.Months, Minor: old.Amount, Currency: strings.ToLower(old.Currency), ProductID: old.ProductID, Merchant: cat.Merchant}
		paid, e := verifiedCheckoutPaid(ctx, v, &old, plan)
		if paid {
			old.Status = "succeeded"
			*r = old
			return true, nil
		}
		if e != nil {
			unknown = e
		}
	}
	return false, unknown
}

// RecordPublicOrderPaid stores a Stripe-confirmed payment on the public order
// and frees its payment window. Caller holds checkout.lock.
func RecordPublicOrderPaid(v *vault.Vault, r *Record) error {
	b, err := v.Get("public-checkout:" + r.RecipientID)
	if err != nil {
		return err
	}
	defer clear(b)
	var saved publicLinkRecord
	if err = json.Unmarshal(b, &saved); err != nil {
		return err
	}
	r.Status = "succeeded"
	if err = persistPublicVerification(v, r); err != nil {
		return err
	}

	a, err := currentActiveCheckout(v)
	if err != nil || a.Released || a.Order.SessionID != r.SessionID {
		return err
	}
	a.Released, a.Order.Status = true, "succeeded"
	return saveActiveCheckout(v, a)
}
