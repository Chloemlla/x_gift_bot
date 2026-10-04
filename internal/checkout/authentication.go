package checkout

import (
	"database/sql"
	"errors"
	"strconv"
	"xgift/internal/vault"
)

// Authentication challenges must stop automated use of the submitted card,
// without treating the pending payment as declined or allowing a second charge.
// The marker makes repeated reconciliation of the same attempt idempotent.
func markAuthenticationRequired(v *vault.Vault, r *Record) error {
	if r.Status != "requires_action" || r.CardFingerprint == "" || r.SessionID == "" || r.SubmittedAt == 0 {
		return nil
	}
	paymentRouteMu.Lock()
	defer paymentRouteMu.Unlock()
	key := "authentication-cooldown:" + r.SessionID + ":" + strconv.Itoa(r.RecoveryAttempts)
	if raw, err := v.Get(key); err == nil {
		clear(raw)
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err := coolPaymentCardLocked(v, r.CardFingerprint, "requires_action"); err != nil {
		return err
	}
	rotation, err := readPaymentRotation(v)
	if err != nil {
		return err
	}
	if rotation != nil && rotation.CardFingerprint == r.CardFingerprint {
		rotation.Used = PaymentBatchSize
		if err := savePaymentRotation(v, rotation); err != nil {
			return err
		}
	}
	return v.Put(key, []byte(`{"applied":true}`))
}
