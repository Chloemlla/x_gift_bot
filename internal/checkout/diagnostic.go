package checkout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"xgift/internal/vault"
)

// Inspect only reads the existing order and asks Stripe for its current status.
func Inspect(ctx context.Context, v *vault.Vault, user string, port, months int) error {
	return inspect(ctx, v, user, port, months, false)
}

// RetireCanceled never creates or confirms a payment. Caller must hold checkout.lock.
func RetireCanceled(ctx context.Context, v *vault.Vault, user string, port, months int) error {
	return inspect(ctx, v, user, port, months, true)
}

func inspect(ctx context.Context, v *vault.Vault, user string, port, months int, retire bool) error {
	user = strings.ToLower(strings.TrimPrefix(user, "@"))
	x, e := newXClient(v, port)
	if e != nil {
		return e
	}
	defer x.close()
	id, e := x.identity(ctx, user, false)
	if e != nil {
		return e
	}
	b, e := v.Get("checkout:" + id)
	if e != nil {
		return e
	}
	defer clear(b)
	var r Record
	if e = json.Unmarshal(b, &r); e != nil {
		return e
	}
	plan, e := planFor(months)
	if e != nil {
		return e
	}
	if r.RecipientID != id || r.Months != plan.Months || r.Amount != plan.Minor || r.Currency != "BDT" || r.ProductID != plan.ProductID {
		return errors.New("recorded recipient or plan mismatch")
	}
	if !sessionURL(r.URL, r.SessionID) {
		return errors.New("untrusted recorded checkout")
	}
	s, e := newStripe(v, port)
	if e != nil {
		return e
	}
	defer s.close()
	var raw json.RawMessage
	if e = s.call(ctx, "POST", "payment_pages/"+r.SessionID+"/init", url.Values{"browser_locale": {"en"}, "redirect_type": {"url"}}, "", &raw); e != nil {
		var se *stripeError
		if !errors.As(e, &se) || se.Code != "checkout_not_active_session" {
			return e
		}
		if e = s.call(ctx, "GET", "payment_pages/"+r.SessionID, url.Values{}, "", &raw); e != nil {
			var inactive *stripeError
			if errors.As(e, &inactive) && inactive.Code == "checkout_not_active_session" {
				return inspectInactiveIntent(ctx, v, s, &r, plan, b, retire)
			}
			return e
		}
	}
	defer clear(raw)
	if e = v.Put("stripe-inspection", raw); e != nil {
		return e
	}
	var p paymentPage
	if e = json.Unmarshal(raw, &p); e != nil {
		return e
	}
	guard := p.guard(&r, plan, false)
	fmt.Printf("ledger=%s stripe_session=%s payment_status=%s amount_minor=%d currency=%s intent_present=%t guard=%v\n", r.Status, p.Status, p.PaymentStatus, p.Total.Total, p.Currency, p.Intent != nil, guard)
	if p.Intent != nil {
		fmt.Printf("intent_status=%s amount_received_minor=%d\n", p.Intent.Status, p.Intent.AmountReceived)
	}
	fmt.Printf("order_age_seconds=%d\n", time.Now().Unix()-r.Created)
	var all map[string]json.RawMessage
	json.Unmarshal(raw, &all)
	for _, key := range []string{"last_payment_error", "error", "payment_method_types", "payment_method_collection", "payment_method_options", "payment_intent", "captcha", "requirements", "status", "payment_status"} {
		if b := all[key]; len(b) > 0 {
			if key == "payment_intent" {
				continue
			}
			fmt.Printf("field=%s present=true bytes=%d\n", key, len(b))
		}
	}
	if retire {
		return errors.New("order is still active; refusing retirement")
	}
	return guard
}

// inspectInactiveIntent reads the previously linked PaymentIntent without confirming it.
func inspectInactiveIntent(ctx context.Context, v *vault.Vault, s *stripeClient, r *Record, plan Plan, original []byte, retire bool) error {
	snapshot, e := v.Get("stripe-inspection")
	if e != nil {
		return e
	}
	defer clear(snapshot)
	var previous paymentPage
	if e = json.Unmarshal(snapshot, &previous); e != nil {
		return e
	}
	if e = previous.guard(r, plan, false); e != nil {
		return e
	}
	var envelope struct {
		Intent struct {
			ID     string `json:"id"`
			Secret string `json:"client_secret"`
		} `json:"payment_intent"`
	}
	if e = json.Unmarshal(snapshot, &envelope); e != nil {
		return e
	}
	intent := envelope.Intent
	if previous.Intent == nil || previous.Intent.ID != intent.ID || !regexp.MustCompile(`^pi_[A-Za-z0-9]+$`).MatchString(intent.ID) || !strings.HasPrefix(intent.Secret, intent.ID+"_secret_") {
		return errors.New("no verified linked PaymentIntent secret available")
	}
	var raw json.RawMessage
	if e = s.call(ctx, "GET", "payment_intents/"+intent.ID, url.Values{"client_secret": {intent.Secret}}, "", &raw); e != nil {
		return e
	}
	defer clear(raw)
	var result struct {
		ID, Status, Currency string
		Live                 bool `json:"livemode"`
		Amount               int
		Received             *int            `json:"amount_received"`
		Capturable           *int            `json:"amount_capturable"`
		Reason               string          `json:"cancellation_reason"`
		LatestCharge         json.RawMessage `json:"latest_charge"`
	}
	if e = json.Unmarshal(raw, &result); e != nil {
		return e
	}
	if result.Received == nil || result.Capturable == nil {
		return errors.New("PaymentIntent is missing explicit amount evidence")
	}
	if !result.Live || result.ID != intent.ID || result.Amount != plan.Minor || result.Currency != "bdt" {
		return errors.New("retrieved PaymentIntent identity or amount mismatch")
	}
	if e = v.Put("stripe-intent-inspection:"+r.RecipientID, raw); e != nil {
		return e
	}
	fmt.Printf("inactive_checkout_intent_status=%s amount_minor=%d received_minor=%d capturable_minor=%d cancellation_reason=%s latest_charge_present=%t\n", result.Status, result.Amount, *result.Received, *result.Capturable, result.Reason, !nullJSON(result.LatestCharge))
	if retire {
		if r.Status == "succeeded" || result.Status != "canceled" || *result.Received != 0 || *result.Capturable != 0 || string(result.LatestCharge) != "null" {
			return errors.New("order is not conclusively canceled and unpaid; refusing retirement")
		}
		proof, err := json.Marshal(struct {
			Record           json.RawMessage `json:"record"`
			CheckoutSnapshot json.RawMessage `json:"checkout_snapshot"`
			CanceledIntent   json.RawMessage `json:"canceled_intent"`
			VerifiedAt       int64           `json:"verified_at"`
		}{original, snapshot, raw, time.Now().Unix()})
		if err != nil {
			return err
		}
		defer clear(proof)
		if e = v.Archive("checkout:"+r.RecipientID, "checkout-retired:"+r.RecipientID+":"+r.SessionID, original, proof); e != nil {
			return e
		}
		fmt.Println("Canceled unpaid order archived; no payment submitted.")
	}
	return nil
}
