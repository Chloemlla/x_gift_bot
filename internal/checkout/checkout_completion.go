package checkout

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"xgift/internal/vault"
)

// rememberVerifiedCheckout preserves the merchant/product/price binding before
// a visitor pays outside this process. Public payments have no server-side
// confirmation request, so verifySubmission cannot be used for their result.
func rememberVerifiedCheckout(v *vault.Vault, r *Record, plan Plan, page *paymentPage) error {
	if err := page.guard(r, plan, false); err != nil {
		return err
	}
	raw := page.raw
	if len(raw) == 0 {
		var err error
		raw, err = json.Marshal(page)
		if err != nil {
			return err
		}
	}
	return v.Put("checkout-verification:"+r.SessionID, raw)
}

func verifiedCheckoutPaid(ctx context.Context, v *vault.Vault, r *Record, plan Plan) (bool, error) {
	s, err := newStripe(ctx, v, r.RecipientID, paymentRead)
	if err != nil {
		return false, err
	}
	defer s.close()
	return s.verifiedCheckoutPaid(ctx, r, plan)
}

func (s *stripeClient) verifiedCheckoutPaid(ctx context.Context, r *Record, plan Plan) (bool, error) {
	if !sessionURL(r.URL, r.SessionID) || r.Months != plan.Months || r.Amount != plan.Minor || !strings.EqualFold(r.Currency, plan.Currency) || r.ProductID != plan.ProductID || plan.Merchant == "" {
		return false, errors.New("checkout completion binding is invalid")
	}
	raw, err := s.vault.Get("checkout-verification:" + r.SessionID)
	if err != nil {
		return s.readBrowserPagePaid(ctx, r, plan)
	}
	defer clear(raw)
	var original paymentPage
	if err = json.Unmarshal(raw, &original); err != nil {
		return false, err
	}
	if err = original.guard(r, plan, false); err != nil {
		return false, err
	}
	// The completed session's init endpoint may no longer be available. Read the
	// result endpoint directly, anchored to the previously guarded full snapshot.
	var result json.RawMessage
	if err = s.call(ctx, "GET", "payment_pages/"+r.SessionID+"/poll", url.Values{}, "", &result); err != nil {
		if inactiveCheckout(err) {
			return s.readLinkedBrowserIntent(ctx, r, plan, &original)
		}
		return false, err
	}
	defer clear(result)
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
	if err = json.Unmarshal(result, &p); err != nil {
		return false, err
	}
	if p.SessionID != r.SessionID || !p.Live || p.Sandbox == nil || *p.Sandbox || p.Mode != "payment" || p.SuccessURL != "https://x.com/"+r.Username+"/gift-premium/success" {
		return false, errors.New("checkout completion session or recipient mismatch")
	}
	if (p.Currency != nil && *p.Currency != plan.Currency) || (p.Amount != nil && *p.Amount != plan.Minor) || (p.AccountID != nil && *p.AccountID != plan.Merchant) {
		return false, errors.New("checkout completion price or merchant mismatch")
	}
	if p.State != "succeeded" || p.PaymentStatus != "succeeded" {
		return false, nil
	}
	if err = s.vault.Put("checkout-completion:"+r.SessionID, result); err != nil {
		return false, err
	}
	return true, nil
}

// Browser payments lack local confirmation evidence. A current full response
// must still bind the session, merchant, recipient, product and exact price.
func (s *stripeClient) readBrowserPagePaid(ctx context.Context, r *Record, plan Plan) (bool, error) {
	page, err := s.page(ctx, r, false)
	if err != nil {
		return false, err
	}
	if err = page.guard(r, plan, false); err != nil {
		return false, err
	}
	if err = rememberVerifiedCheckout(s.vault, r, plan, page); err != nil {
		return false, err
	}
	if page.Status != "complete" || page.PaymentStatus != "paid" {
		return false, nil
	}
	if page.Intent == nil || !regexp.MustCompile(`^pi_[A-Za-z0-9]+$`).MatchString(page.Intent.ID) {
		return false, errors.New("completed browser checkout lacks linked intent evidence")
	}
	return true, nil
}

// Inactivity is never treated as unpaid. Read an intent only using a secret
// contained in the previously guarded snapshot of this exact checkout.
func (s *stripeClient) readLinkedBrowserIntent(ctx context.Context, r *Record, plan Plan, page *paymentPage) (bool, error) {
	if err := page.guard(r, plan, false); err != nil {
		return false, err
	}
	var envelope struct {
		Intent *stripeIntentEvidence `json:"payment_intent"`
	}
	if err := json.Unmarshal(page.raw, &envelope); err != nil {
		return false, err
	}
	intent := envelope.Intent
	if page.Intent == nil || intent == nil || intent.ID != page.Intent.ID || !regexp.MustCompile(`^pi_[A-Za-z0-9]+$`).MatchString(intent.ID) || !strings.HasPrefix(intent.ClientSecret, intent.ID+"_secret_") || len(intent.ClientSecret) <= len(intent.ID+"_secret_") {
		return false, errors.New("inactive checkout lacks verified linked intent")
	}
	var result stripeIntentEvidence
	if err := s.call(ctx, "GET", "payment_intents/"+intent.ID, url.Values{"client_secret": {intent.ClientSecret}}, "", &result); err != nil {
		return false, err
	}
	if result.ID != intent.ID || !result.Live || result.Amount != plan.Minor || result.Currency != plan.Currency || result.Received == nil {
		return false, errors.New("browser payment intent identity or amount mismatch")
	}
	if result.Status != "succeeded" || *result.Received != plan.Minor {
		return false, errors.New("browser payment outcome remains unconfirmed")
	}
	return true, nil
}
