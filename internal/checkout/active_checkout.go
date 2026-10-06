package checkout

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"xgift/internal/vault"
)

const activeCheckoutKey = "checkout-creation:active"

// CheckoutWaitError is a retryable creation wait. Reading an existing verified
// public link does not require a new creation reservation.
type CheckoutWaitError struct{ Wait time.Duration }

func (e *CheckoutWaitError) Error() string {
	return "checkout creation is waiting for the active payment window"
}
func (e *CheckoutWaitError) Unwrap() error { return ErrCheckoutRateLimited }

type activeCheckout struct {
	Order     Record `json:"order"`
	Plan      Plan   `json:"plan"`
	ExpiresAt int64  `json:"expires_at"`
	Released  bool   `json:"released"`
}

func saveActiveCheckout(v *vault.Vault, a activeCheckout) error {
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return v.Put(activeCheckoutKey, b)
}
func readActiveCheckout(v *vault.Vault) (activeCheckout, error) {
	var a activeCheckout
	b, err := v.Get(activeCheckoutKey)
	if err != nil {
		return a, err
	}
	defer clear(b)
	if err = json.Unmarshal(b, &a); err != nil {
		return a, err
	}
	// Accept the previous fixed 15-minute format, but apply the new deadline.
	// Never reset the creation clock when upgrading or restarting.
	deadline := time.Unix(a.Order.Created, 0).Add(publicLinkTTL).UnixMilli()
	legacyDeadline := time.Unix(a.Order.Created, 0).Add(15 * time.Minute).UnixMilli()
	if a.ExpiresAt != 0 && (a.Order.SessionID == "" || a.Order.Created <= 0 || (a.ExpiresAt != deadline && a.ExpiresAt != legacyDeadline)) {
		return a, errors.New("invalid active checkout reservation")
	}
	if a.ExpiresAt != 0 {
		a.ExpiresAt = deadline
	}
	return a, nil
}

// Bootstrap once under checkout.lock so the first post-upgrade creation cannot
// invalidate the most recently published legacy public link. Unknown outcomes
// stay protected until their fixed deadline; only verified paid evidence frees it.
func bootstrapActiveCheckout(v *vault.Vault, now time.Time) (activeCheckout, error) {
	a, err := readActiveCheckout(v)
	if !errors.Is(err, sql.ErrNoRows) {
		return a, err
	}
	for _, prefix := range []string{"public-checkout:", "checkout:"} {
		names, err := v.NamesWithPrefix(prefix)
		if err != nil {
			return a, err
		}
		for _, name := range names {
			b, e := v.Get(name)
			if e != nil {
				return a, e
			}
			var r Record
			if prefix == "public-checkout:" {
				var saved publicLinkRecord
				e = json.Unmarshal(b, &saved)
				r = saved.Order
			} else {
				e = json.Unmarshal(b, &r)
			}
			clear(b)
			if e != nil {
				return a, e
			}
			if r.Status == "succeeded" || !publicLinkFresh(&r, now) || CheckoutLink(&r) == "" {
				continue
			}
			if prefix == "public-checkout:" && (r.Status != "created" || !unsubmitted(&r) || r.CardFingerprint != "") {
				continue
			}
			if r.Created > a.Order.Created {
				a.Order = r
			}
		}
	}
	if a.Order.SessionID != "" {
		r := a.Order
		a.Plan = Plan{Months: r.Months, Minor: r.Amount, Currency: strings.ToLower(r.Currency), ProductID: r.ProductID}
		if cat, e := ReadCatalog(v); e == nil {
			a.Plan.Merchant = cat.Merchant
		}
		a.ExpiresAt = time.Unix(r.Created, 0).Add(publicLinkTTL).UnixMilli()
	}
	return a, saveActiveCheckout(v, a)
}

// Callers hold checkout.lock. A successful publication reserves its original
// creation timestamp, never a sliding interval from subsequent cache retrievals.
func holdPublicCheckout(v *vault.Vault, r *Record, p Plan, now time.Time) error {
	if r.Status != "created" || !publicLinkFresh(r, now) {
		return nil
	}
	current, err := bootstrapActiveCheckout(v, now)
	if err != nil {
		return err
	}
	if !current.Released && current.ExpiresAt > now.UnixMilli() && current.Order.SessionID != r.SessionID {
		return nil
	}
	if current.Order.SessionID == r.SessionID && current.Released {
		return nil
	}
	return saveActiveCheckout(v, activeCheckout{Order: *r, Plan: p, ExpiresAt: time.Unix(r.Created, 0).Add(publicLinkTTL).UnixMilli()})
}

// CheckoutCreationWait reports the persisted maximum wait without contacting
// Stripe. The creation path performs live checks to release paid orders early.
func CheckoutCreationWait(v *vault.Vault, now time.Time) (time.Duration, error) {
	var wait time.Duration
	a, err := readActiveCheckout(v)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if err == nil && !a.Released {
		wait = time.UnixMilli(a.ExpiresAt).Sub(now)
	}
	if wait < 0 {
		wait = 0
	}
	b, err := v.Get("checkout-creation:last")
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if err == nil {
		var last int64
		if err = json.Unmarshal(b, &last); err != nil {
			return 0, err
		}
		if remaining := time.UnixMilli(last).Add(checkoutCreationInterval).Sub(now); remaining > wait {
			wait = remaining
		}
	}
	return wait, nil
}

func (x *xClient) checkCreation(ctx context.Context, now time.Time) error {
	a, err := bootstrapActiveCheckout(x.vault, now)
	if err != nil {
		return err
	}
	if !a.Released && a.ExpiresAt != 0 {
		if x.publicReplacement != "" && a.Order.SessionID == x.publicReplacement && a.Order.CardFingerprint == "" && unsubmitted(&a.Order) {
			return checkCheckoutCreation(x.vault, time.Now())
		}
		r := a.Order
		verificationErr := verifyPublicCheckout(ctx, x.vault, x, &r, a.Plan)
		verified := verificationErr == nil && r.Status == "succeeded"
		if !verified {
			paid, _ := x.checkoutPaid(ctx, &r, a.Plan)
			if paid {
				r.Status = "succeeded"
				verified = true
			}
		}
		if verified {
			a.Released = true
			a.Order = r
			if err := saveActiveCheckout(x.vault, a); err != nil {
				return err
			}
		} else if remaining := time.Until(time.UnixMilli(a.ExpiresAt)); remaining > 0 {
			return &CheckoutWaitError{Wait: remaining}
		} else if verificationErr != nil && !errors.Is(verificationErr, ErrVerifyUnpaid) {
			// The 180-second window limits idle checkouts. It is not permission
			// to invalidate an in-flight payment or ignore a failed status read.
			return &CheckoutWaitError{Wait: 10 * time.Second}
		} else {
			// A final live check proved the idle/failed payment can be replaced.
			a.Released = true
			if err := saveActiveCheckout(x.vault, a); err != nil {
				return err
			}
		}
	}
	return checkCheckoutCreation(x.vault, time.Now())
}

// PublicCheckoutWindow identifies only a public order; private payment records
// are never exposed through this queue optimization.
func PublicCheckoutWindow(v *vault.Vault, now time.Time) (string, int, time.Time, error) {
	a, err := readActiveCheckout(v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, time.Time{}, nil
	}
	if err != nil {
		return "", 0, time.Time{}, err
	}
	if a.Released || a.ExpiresAt <= now.UnixMilli() || a.Order.CardFingerprint != "" || !unsubmitted(&a.Order) {
		return "", 0, time.Time{}, nil
	}
	b, err := v.Get("public-checkout:" + a.Order.RecipientID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, time.Time{}, nil
	}
	if err != nil {
		return "", 0, time.Time{}, err
	}
	defer clear(b)
	var saved publicLinkRecord
	if err = json.Unmarshal(b, &saved); err != nil {
		return "", 0, time.Time{}, err
	}
	if saved.Order.SessionID != a.Order.SessionID {
		return "", 0, time.Time{}, nil
	}
	return a.Order.Username, a.Order.Months, time.UnixMilli(a.ExpiresAt), nil
}

func (x *xClient) checkoutPaid(ctx context.Context, r *Record, p Plan) (bool, error) {
	if x.readCheckoutPaid != nil {
		return x.readCheckoutPaid(ctx, r, p)
	}
	if x.readCheckout != nil {
		return false, nil
	}
	return verifiedCheckoutPaid(ctx, x.vault, r, p)
}

// Called under checkout.lock after archiving the user's explicitly replaced
// public order. Never release another order's window.
func (x *xClient) releasePublicReplacement(v *vault.Vault) error {
	a, err := readActiveCheckout(v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if x.publicReplacement != "" && a.Order.SessionID == x.publicReplacement && a.Order.CardFingerprint == "" && unsubmitted(&a.Order) {
		a.Released = true
		return saveActiveCheckout(v, a)
	}
	return nil
}
