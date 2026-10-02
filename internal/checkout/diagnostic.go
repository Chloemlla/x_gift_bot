package checkout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"xgift/internal/vault"
)

// Inspect only reads the existing order and asks Stripe for its current status.
func Inspect(ctx context.Context, v *vault.Vault, user string, port, months int) error {
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
		return e
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
	return guard
}
