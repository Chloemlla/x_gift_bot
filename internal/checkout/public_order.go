package checkout

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
	"xgift/internal/vault"
)

// PublicOrderStatus returns the newest public order for user that was created
// by the browser owning this cookie, or nil. It reads local records only: it
// never contacts X or Stripe and never creates or changes an order.
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
