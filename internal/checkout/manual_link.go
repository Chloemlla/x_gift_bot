package checkout

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"xgift/internal/vault"
)

var ErrManualLinkConflict = errors.New("existing order belongs to another username or plan")
var ErrPaymentActionRequired = errors.New("bank authentication required")

// ManualLinkForUsername creates/reuses a guarded checkout without tokenizing or
// confirming a card. No redemption code is required. Caller holds checkout.lock.
func ManualLinkForUsername(ctx context.Context, v *vault.Vault, user string, port, months int, verifiedUnpaid bool) (*Record, error) {
	user, ok := NormalizeUsername(user)
	if !ok {
		return nil, errors.New("invalid username")
	}
	cat, err := ReadCatalog(v)
	if err != nil {
		return nil, err
	}
	if _, err = cat.PlanFor(months); err != nil {
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
	return manualLinkForRecipient(ctx, v, user, recipient, port, months, verifiedUnpaid)
}

func manualLinkForRecipient(ctx context.Context, v *vault.Vault, user, recipient string, port, months int, verifiedUnpaid bool) (*Record, error) {
	raw, err := v.Get("checkout:" + recipient)
	if errors.Is(err, sql.ErrNoRows) {
		return RunForRecipient(ctx, v, user, recipient, false, port, months)
	}
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	var r Record
	if err = json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	cat, err := ReadCatalog(v)
	if err != nil {
		return nil, err
	}
	plan, err := cat.PlanFor(months)
	if err != nil {
		return nil, err
	}
	if r.Username != user || r.RecipientID != recipient || r.Months != months || r.Amount != plan.Minor || r.Currency != strings.ToUpper(plan.Currency) || r.ProductID != plan.ProductID {
		return nil, ErrManualLinkConflict
	}
	if r.Status == "succeeded" {
		return &r, nil
	}
	if r.Status == "requires_action" {
		current, lookupErr := PrepareRecoveryLinkForRecipient(ctx, v, user, recipient, port, months, false)
		if lookupErr == nil || errors.Is(lookupErr, ErrPaymentActionRequired) {
			return current, nil
		}
		// Only a live Stripe confirmation of terminal cancellation can retire a
		// challenged payment. An operator checkbox cannot override this check.
		if err = RetireCanceledAuthentication(ctx, v, recipient); err != nil {
			return &r, err
		}
		return RunForRecipient(ctx, v, user, recipient, false, port, months)
	}
	return PrepareRecoveryLinkForRecipient(ctx, v, user, recipient, port, months, verifiedUnpaid)
}
