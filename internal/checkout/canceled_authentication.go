package checkout

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"
	"xgift/internal/vault"
)

// RetireCanceledAuthentication archives a challenged payment only after a fresh
// Stripe read proves its linked PaymentIntent reached the terminal canceled
// state. Caller must hold checkout.lock and explicitly authorize replacement.
func RetireCanceledAuthentication(ctx context.Context, v *vault.Vault, recipient string) error {
	original, err := v.Get("checkout:" + recipient)
	if err != nil {
		return err
	}
	defer clear(original)
	var r Record
	if err = json.Unmarshal(original, &r); err != nil {
		return err
	}
	if r.RecipientID != recipient || r.Status != "requires_action" || !sessionURL(r.URL, r.SessionID) {
		return errors.New("order is not a bound authentication challenge")
	}
	cat, err := ReadCatalog(v)
	if err != nil {
		return err
	}
	plan, err := cat.PlanFor(r.Months)
	if err != nil {
		return err
	}
	if r.Amount != plan.Minor || r.Currency != strings.ToUpper(plan.Currency) || r.ProductID != plan.ProductID {
		return errors.New("order price mismatch")
	}
	if err = verifySubmission(v, &r, plan); err != nil {
		return err
	}
	snapshot, err := v.Get("stripe-confirm:" + r.SessionID)
	if err != nil {
		return err
	}
	defer clear(snapshot)
	var page paymentPage
	if err = json.Unmarshal(snapshot, &page); err != nil {
		return err
	}
	if err = page.guard(&r, plan, false); err != nil {
		return err
	}
	var envelope struct {
		Intent struct {
			ID     string `json:"id"`
			Secret string `json:"client_secret"`
			Status string `json:"status"`
		} `json:"payment_intent"`
	}
	if err = json.Unmarshal(snapshot, &envelope); err != nil {
		return err
	}
	i := envelope.Intent
	if page.PaymentStatus != "unpaid" || i.Status != "requires_action" || !regexp.MustCompile(`^pi_[A-Za-z0-9]+$`).MatchString(i.ID) || !strings.HasPrefix(i.Secret, i.ID+"_secret_") {
		return errors.New("missing linked authentication evidence")
	}
	s, err := newStripe(ctx, v, recipient, paymentRead)
	if err != nil {
		return err
	}
	defer s.close()
	state, err := s.poll(ctx, &r, plan)
	if err == nil || !inactiveCheckout(err) {
		return errors.New("original checkout is not conclusively inactive: " + state)
	}
	var raw json.RawMessage
	if err = s.call(ctx, "GET", "payment_intents/"+i.ID, url.Values{"client_secret": {i.Secret}}, "", &raw); err != nil {
		return err
	}
	defer clear(raw)
	if err = guardCanceledAuthentication(raw, i.ID, plan); err != nil {
		return err
	}
	proof, err := json.Marshal(map[string]any{"record": json.RawMessage(original), "checkout_snapshot": json.RawMessage(snapshot), "canceled_intent": raw, "verified_at": time.Now().Unix(), "reason": "operator_replacement_after_canceled_authentication"})
	if err != nil {
		return err
	}
	defer clear(proof)
	return v.Archive("checkout:"+recipient, "checkout-retired:"+recipient+":"+r.SessionID, original, proof)
}

func guardCanceledAuthentication(raw []byte, id string, plan Plan) error {
	var p struct {
		ID, Status, Currency, Object string
		Live                         bool `json:"livemode"`
		Amount                       int
		Received                     *int            `json:"amount_received"`
		Capturable                   *int            `json:"amount_capturable"`
		Charge                       json.RawMessage `json:"latest_charge"`
		Capture                      string          `json:"capture_method"`
		CanceledAt                   int64           `json:"canceled_at"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	// Publishable-key retrieval omits received/capturable/charge fields. Canceled
	// is terminal and cannot be reached from succeeded. Still reject any explicit
	// evidence of funds or a charge, and accept only automatic-capture card flows.
	if p.ID != id || p.Object != "payment_intent" || !p.Live || p.Status != "canceled" || p.Amount != plan.Minor || p.Currency != plan.Currency || p.Capture != "automatic" || p.CanceledAt <= 0 || (p.Received != nil && *p.Received != 0) || (p.Capturable != nil && *p.Capturable != 0) || !nullJSON(p.Charge) {
		return errors.New("linked PaymentIntent is not conclusively canceled without payment evidence")
	}
	return nil
}
