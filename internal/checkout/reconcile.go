package checkout

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"xgift/internal/vault"
)

// Reconcile only queries the existing session. The caller must hold checkout.lock.
func Reconcile(ctx context.Context, v *vault.Vault, recipient string, port int) (*Record, error) {
	raw, err := v.Get("checkout:" + recipient)
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	var r Record
	if err = json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	catalog, err := ReadCatalog(v)
	if err != nil {
		return nil, err
	}
	plan, err := catalog.PlanFor(r.Months)
	if err != nil {
		return nil, err
	}
	if r.RecipientID != recipient || r.Amount != plan.Minor || r.Currency != strings.ToUpper(plan.Currency) || r.ProductID != plan.ProductID || !usernamePattern.MatchString(r.Username) || !sessionURL(r.URL, r.SessionID) {
		return nil, errors.New("recorded order identity or price mismatch")
	}
	if r.Status == "succeeded" {
		return &r, nil
	}
	if r.Status != "unknown" && r.Status != "submitting" && r.Status != "requires_action" && !IsPaymentDeclined(&r) {
		return &r, errors.New("order has no submitted payment to reconcile")
	}
	if err = verifySubmission(v, &r, plan); err != nil {
		return &r, err
	}
	s, err := newStripe(ctx, v, r.RecipientID, paymentRead)
	if err != nil {
		return &r, err
	}
	defer s.close()
	status, err := s.poll(ctx, &r, plan)
	if err != nil {
		return &r, err
	}
	if status == "requires_action" {
		r.Status = status
		if err = save(v, &r); err != nil {
			return &r, err
		}
		if err = markAuthenticationRequired(v, &r); err != nil {
			return &r, err
		}
	}
	if status != "succeeded" {
		return &r, errors.New("payment is not yet confirmed successful")
	}
	r.Status = "succeeded"
	r.LastError = nil
	if err = save(v, &r); err != nil {
		return &r, err
	}
	return &r, paymentOutcome(v, &r)
}

func verifySubmission(v *vault.Vault, r *Record, plan Plan) error {
	f, err := url.ParseQuery(r.ConfirmParameters)
	if err != nil || len(f) != 5 || r.SubmittedAt < r.Created || r.SubmittedAt == 0 || !regexp.MustCompile(`^pm_[A-Za-z0-9]+$`).MatchString(r.PaymentMethod) || r.ConfirmKey != idempotency(r, "confirm") {
		return errors.New("missing or inconsistent original confirmation evidence")
	}
	expected := map[string]string{"payment_method": r.PaymentMethod, "expected_amount": strconv.Itoa(plan.Minor), "expected_payment_method_type": "card", "init_checksum": f.Get("init_checksum"), "return_url": "https://x.com/" + r.Username + "/gift-premium/success"}
	if expected["init_checksum"] == "" {
		return errors.New("missing original checksum")
	}
	for k, value := range expected {
		if len(f[k]) != 1 || f.Get(k) != value {
			return errors.New("original confirmation parameters mismatch")
		}
	}
	// Legacy records were produced by the same guard-before-confirm flow, but did
	// not retain a separate snapshot. Never manufacture historical Stripe evidence.
	if r.ManualRecovery {
		b, err := v.Get(manualProofKey(r))
		if err != nil {
			return err
		}
		defer clear(b)
		var proof manualProof
		if err = json.Unmarshal(b, &proof); err != nil {
			return err
		}
		if err = proof.guard(r, plan); err != nil {
			return err
		}
		var page paymentPage
		if err = json.Unmarshal(proof.Page, &page); err != nil {
			return err
		}
		if page.Checksum != f.Get("init_checksum") {
			return errors.New("manual preflight checksum mismatch")
		}
		return nil
	}
	if r.PreflightSaved {
		raw, err := v.Get("stripe-preflight:" + r.SessionID)
		if err != nil {
			return err
		}
		defer clear(raw)
		var p paymentPage
		if err = json.Unmarshal(raw, &p); err != nil {
			return err
		}
		if err = p.guard(r, plan, true); err != nil {
			return err
		}
		if p.Checksum != f.Get("init_checksum") {
			return errors.New("preflight checksum mismatch")
		}
	}
	return nil
}

func (s *stripeClient) poll(ctx context.Context, r *Record, plan Plan) (string, error) {
	if err := verifySubmission(s.vault, r, plan); err != nil {
		return "", err
	}
	var raw json.RawMessage
	if err := s.call(ctx, "GET", "payment_pages/"+r.SessionID+"/poll", url.Values{}, "", &raw); err != nil {
		return "", err
	}
	defer clear(raw)
	var p struct {
		SessionID     string  `json:"session_id"`
		Live          bool    `json:"livemode"`
		Sandbox       *bool   `json:"is_sandbox_merchant"`
		Mode          string  `json:"mode"`
		State         string  `json:"state"`
		PaymentStatus string  `json:"payment_object_status"`
		SuccessURL    string  `json:"success_url"`
		Currency      *string `json:"currency"`
		Amount        *int    `json:"amount"`
		AccountID     *string `json:"account_id"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", err
	}
	if p.SessionID != r.SessionID || !p.Live || p.Sandbox == nil || *p.Sandbox || p.Mode != "payment" || p.SuccessURL != "https://x.com/"+r.Username+"/gift-premium/success" {
		return "", errors.New("Stripe result session or recipient mismatch")
	}
	if (p.Currency != nil && *p.Currency != plan.Currency) || (p.Amount != nil && *p.Amount != plan.Minor) || (p.AccountID != nil && *p.AccountID != plan.Merchant) {
		return "", errors.New("Stripe result price or merchant mismatch")
	}
	if err := s.vault.Put("stripe-result:"+r.SessionID, raw); err != nil {
		return "", err
	}
	if p.State == "succeeded" && p.PaymentStatus == "succeeded" {
		return "succeeded", nil
	}
	if p.PaymentStatus == "requires_action" {
		return "requires_action", nil
	}
	return "pending", nil
}
