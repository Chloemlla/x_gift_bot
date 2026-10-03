package checkout

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"xgift/internal/vault"
)

// ResumeForRecipient is only called after an explicit redemption submission,
// with checkout.lock held and the same code still bound to this recipient.
// Submitted payments are reconciled, never sent through the payment flow again.
func ResumeForRecipient(ctx context.Context, v *vault.Vault, user, recipient string, port, months int) (*Record, error) {
	if recipient == "" {
		return nil, errors.New("bound recipient is required")
	}
	user = strings.ToLower(strings.TrimPrefix(user, "@"))
	catalog, err := ReadCatalog(v)
	if err != nil {
		return nil, err
	}
	plan, err := catalog.PlanFor(months)
	if err != nil {
		return nil, err
	}
	raw, err := v.Get("checkout:" + recipient)
	if errors.Is(err, sql.ErrNoRows) {
		// A failure before the first durable order record cannot have submitted
		// payment: submission always persists its evidence before the request.
		return RunForRecipient(ctx, v, user, recipient, true, port, months)
	}
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	var r Record
	if json.Unmarshal(raw, &r) != nil || r.Username != user || r.RecipientID != recipient || r.Months != months || r.Amount != plan.Minor || r.Currency != strings.ToUpper(plan.Currency) || r.ProductID != plan.ProductID {
		return nil, errors.New("bound order identity or plan mismatch")
	}
	if IsPaymentDeclined(&r) {
		return &r, ErrPaymentDeclined
	}
	if r.Status == "succeeded" || r.Status == "submitting" || r.Status == "unknown" || r.Status == "requires_action" {
		return Reconcile(ctx, v, recipient, port)
	}
	if !unsubmitted(&r) {
		return &r, errors.New("order contains payment evidence; refusing another submission")
	}
	if r.Status != "created" && r.Status != "creating" {
		return &r, errors.New("order cannot be resumed from this state")
	}
	// The ordinary flow rechecks X identity, eligibility and price, then Stripe
	// session, merchant, product, amount, unpaid state and null PaymentIntent.
	result, err := RunForRecipient(ctx, v, user, recipient, true, port, months)
	if result == nil {
		return &r, err
	}
	return result, err
}

func unsubmitted(r *Record) bool {
	return r.PaymentMethod == "" && r.ConfirmParameters == "" && r.ConfirmKey == "" && r.SubmittedAt == 0 && !r.PreflightSaved
}
