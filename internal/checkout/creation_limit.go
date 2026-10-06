package checkout

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
	"xgift/internal/vault"
)

var ErrCheckoutRateLimited = errors.New("checkout creation must be spaced at least 15 seconds apart")

const checkoutCreationInterval = 15 * time.Second

// Callers hold checkout.lock across this check, the reservation and X mutation.
// Persist before sending: failures and process restarts must not bypass the limit.
func reserveCheckoutCreation(v *vault.Vault, now time.Time) error {
	if err := checkCheckoutCreation(v, now); err != nil {
		return err
	}
	b, _ := json.Marshal(now.UnixMilli())
	return v.Put("checkout-creation:last", b)
}

func checkCheckoutCreation(v *vault.Vault, now time.Time) error {
	b, err := v.Get("checkout-creation:last")
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		var last int64
		if err = json.Unmarshal(b, &last); err != nil {
			return err
		}
		if now.Sub(time.UnixMilli(last)) < checkoutCreationInterval {
			return &CheckoutWaitError{Wait: time.UnixMilli(last).Add(checkoutCreationInterval).Sub(now)}
		}
	}
	return nil
}
