package checkout

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"xgift/internal/vault"
)

var ErrPaymentPaused = errors.New("payments paused after card failures; administrator action required")
var ErrPaymentDeclined = errors.New("payment was declined; automatic resubmission is blocked")

const paymentInterval = 30 * time.Second

type paymentControl struct {
	CardFingerprint     string `json:"card_fingerprint"`
	LastSubmittedAt     int64  `json:"last_submitted_at"`
	LastAttempt         int    `json:"last_attempt,omitempty"`
	LastSession         string `json:"last_session"`
	Outcome             string `json:"outcome"`
	ConsecutiveDeclines int    `json:"consecutive_declines"`
	Paused              bool   `json:"paused"`
	Reason              string `json:"reason,omitempty"`
}

func readPaymentControl(v *vault.Vault) (paymentControl, error) {
	var state paymentControl
	b, err := v.Get("payment-control")
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	defer clear(b)
	if err = json.Unmarshal(b, &state); err != nil {
		return state, errors.New("invalid payment control record")
	}
	if state.CardFingerprint != "" {
		fingerprint, err := paymentCardFingerprint(v)
		if err != nil {
			return state, err
		}
		if fingerprint != state.CardFingerprint {
			state = paymentControl{CardFingerprint: fingerprint, LastSubmittedAt: state.LastSubmittedAt}
		}
	}
	return state, nil
}
func paymentCardFingerprint(v *vault.Vault) (string, error) {
	cards, err := readCards(v)
	if err != nil {
		return "", err
	}
	return cardSetFingerprint(cards), nil
}
func savePaymentControl(v *vault.Vault, state paymentControl) error {
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	defer clear(b)
	return v.Put("payment-control", b)
}

// PaymentPaused is read-only and is shared by the website health and admission
// checks. A changed default card clears the old card's circuit on the next write.
func PaymentPaused(v *vault.Vault) (bool, error) {
	state, err := readPaymentControl(v)
	return state.Paused, err
}

// Caller holds checkout.lock for the full flow, including this durable slot
// reservation. Both the CLI and web payment flow use this function.
func reservePaymentSlot(ctx context.Context, v *vault.Vault, r *Record) error {
	state, err := readPaymentControl(v)
	if err != nil {
		return err
	}
	if state.Paused {
		return ErrPaymentPaused
	}
	if state.LastSession == r.SessionID && state.LastAttempt == r.RecoveryAttempts {
		return errors.New("payment session already reserved; reconcile instead of submitting")
	}
	if delay := paymentWait(state, time.Now()); delay > 0 {
		progress(ctx, 70, fmt.Sprintf("正在等待付款间隔（约 %d 秒），请勿重复提交…", int(delay.Seconds())+1))
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	state.CardFingerprint, err = paymentCardFingerprint(v)
	if err != nil {
		return err
	}
	state.LastSubmittedAt = time.Now().UnixNano()
	state.LastSession = r.SessionID
	state.LastAttempt = r.RecoveryAttempts
	state.Outcome = ""
	return savePaymentControl(v, state)
}
func paymentWait(state paymentControl, now time.Time) time.Duration {
	if state.LastSubmittedAt == 0 {
		return 0
	}
	delay := time.Unix(0, state.LastSubmittedAt).Add(paymentInterval).Sub(now)
	if delay < 0 {
		return 0
	}
	return delay
}
func paymentOutcome(v *vault.Vault, r *Record) error {
	state, err := readPaymentControl(v)
	if err != nil {
		return err
	}
	// Old reconciliations must not clear a pause caused by newer payments.
	if state.LastSession != r.SessionID || state.LastAttempt != r.RecoveryAttempts || state.Outcome != "" {
		return nil
	}
	if r.Status == "succeeded" {
		state.ConsecutiveDeclines = 0
		state.Outcome = "succeeded"
		state.Reason = ""
	} else if IsPaymentDeclined(r) {
		state.ConsecutiveDeclines++
		state.Outcome = "declined"
		// The declined order keeps its evidence; the running card+node batch is
		// expired so the next attempt rotates to another pair.
		if err = markPaymentDecline(v, r.RecipientID); err != nil {
			return err
		}
		// Ordinary card declines stay local to the failed order. Only an explicit
		// provider no-retry instruction blocks that one card; with other cards
		// configured the site keeps paying through the remaining rotation and
		// pauses only when there is nothing left to try.
		if r.LastError != nil && r.LastError.AdviceCode == "do_not_try_again" {
			fingerprint := declinedCardFingerprint(v, r.RecipientID)
			if fingerprint == "" {
				state.Paused = true
				state.Reason = "do_not_try_again"
			} else {
				if err = blockPaymentCard(v, fingerprint, "do_not_try_again"); err != nil {
					return err
				}
				remaining, e := hasUsableCard(v)
				if e != nil {
					return e
				}
				if !remaining {
					state.Paused = true
					state.Reason = "do_not_try_again"
				}
			}
		}
	} else {
		return nil
	}
	return savePaymentControl(v, state)
}

// declinedCardFingerprint resolves the card bound to a declined order without
// selecting, rotating or contacting any node.
func declinedCardFingerprint(v *vault.Vault, recipient string) string {
	route, err := readPaymentRoute(v, recipient)
	if err != nil || route.Card == "" {
		return ""
	}
	return route.Card
}
func IsPaymentDeclined(r *Record) bool {
	return r != nil && r.Status != "succeeded" && r.Status != "requires_action" && (r.Status == "declined" || r.LastError != nil && r.LastError.HTTP == 402 && r.LastError.Type == "card_error")
}

// ResetPaymentPause requires an explicit operator action and checkout.lock.
// The time of the last attempt is retained so a reset cannot bypass spacing.
func ResetPaymentPause(v *vault.Vault) error {
	state, err := readPaymentControl(v)
	if err != nil {
		return err
	}
	state.Paused = false
	state.ConsecutiveDeclines = 0
	state.Reason = ""
	return savePaymentControl(v, state)
}
