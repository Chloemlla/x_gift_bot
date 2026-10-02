package checkout

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"xgift/internal/vault"
)

type Record struct {
	Username    string `json:"username"`
	RecipientID string `json:"recipient_id"`
	Months      int    `json:"months"`
	Amount      int    `json:"amount_minor"`
	Currency    string `json:"currency"`
	ProductID   string `json:"product_id"`
	SessionID   string `json:"session_id"`
	URL         string `json:"url"`
	Status      string `json:"status"`
	Created     int64  `json:"created"`
}

var sessionPattern = regexp.MustCompile(`^cs_live_[A-Za-z0-9]+$`)

func sessionURL(link, id string) bool {
	u, e := url.Parse(link)
	return e == nil && sessionPattern.MatchString(id) && u.Scheme == "https" && u.Host == "checkout.stripe.com" && u.User == nil && u.RawQuery == "" && (u.Path == "/g/pay/"+id || u.Path == "/c/pay/"+id)
}
func save(v *vault.Vault, r *Record) error {
	b, e := json.Marshal(r)
	if e != nil {
		return e
	}
	return v.Put("checkout:"+r.RecipientID, b)
}
func Run(ctx context.Context, v *vault.Vault, user string, pay bool, port, months int) (*Record, error) {
	return run(ctx, v, user, "", pay, port, months)
}

// RunForRecipient pins a redemption to the identity checked before reserving its code.
func RunForRecipient(ctx context.Context, v *vault.Vault, user, expectedRecipient string, pay bool, port, months int) (*Record, error) {
	if expectedRecipient == "" {
		return nil, errors.New("expected recipient is required")
	}
	return run(ctx, v, user, expectedRecipient, pay, port, months)
}

func run(ctx context.Context, v *vault.Vault, user, expectedRecipient string, pay bool, port, months int) (*Record, error) {
	user = strings.ToLower(strings.TrimPrefix(user, "@"))
	if !regexp.MustCompile(`^[a-z0-9_]{1,15}$`).MatchString(user) {
		return nil, errors.New("invalid username")
	}
	plan, e := planFor(months)
	if e != nil {
		return nil, e
	}
	x, e := newXClient(v, port)
	if e != nil {
		return nil, e
	}
	defer x.close()
	recipient, e := x.recipient(ctx, user)
	if e != nil {
		return nil, e
	}
	if expectedRecipient != "" && recipient != expectedRecipient {
		return nil, errors.New("recipient identity changed; refusing to create or pay an order")
	}
	// Fail closed on legacy records rather than silently bypassing an earlier attempt.
	if _, e = v.Get("checkout:" + user); e == nil {
		return nil, errors.New("legacy checkout requires migration before creating another order")
	} else if !errors.Is(e, sql.ErrNoRows) {
		return nil, e
	}
	var r Record
	raw, e := v.Get("checkout:" + recipient)
	if e == nil {
		if json.Unmarshal(raw, &r) != nil || r.RecipientID != recipient || r.Months != plan.Months || r.Amount != plan.Minor || r.Currency != "BDT" || r.ProductID != plan.ProductID || !sessionURL(r.URL, r.SessionID) {
			return nil, errors.New("existing checkout differs from this recipient or plan; refusing another order")
		}
	} else if !errors.Is(e, sql.ErrNoRows) {
		return nil, e
	}
	if r.Status == "succeeded" {
		return &r, nil
	}
	if r.Status != "" && r.Status != "created" && r.Status != "creating" {
		return &r, fmt.Errorf("existing checkout status is %s; no payment will be resubmitted", r.Status)
	}
	if r.Status == "creating" {
		return &r, errors.New("previous checkout creation outcome is unknown; inspect it before creating another")
	}
	if e = x.quote(ctx, user, plan); e != nil {
		return nil, e
	}
	if r.SessionID == "" {
		r = Record{Username: user, RecipientID: recipient, Months: plan.Months, Amount: plan.Minor, Currency: "BDT", ProductID: plan.ProductID, Status: "creating", Created: time.Now().Unix()}
		// Persist before creating the external session. A crash must not silently create a second session.
		if e = save(v, &r); e != nil {
			return nil, e
		}
		r.SessionID, r.URL, e = x.create(ctx, user, recipient, plan)
		if e != nil {
			return &r, e
		}
		r.Status = "created"
		if e = save(v, &r); e != nil {
			return &r, e
		}
	}
	s, e := newStripe(v, port)
	if e != nil {
		return &r, e
	}
	defer s.close()
	page, e := s.page(ctx, &r, true)
	if e != nil {
		return &r, e
	}
	if e = page.guard(&r, plan, false); e != nil {
		return &r, e
	}
	if page.PaymentStatus == "paid" && page.Status == "complete" {
		r.Status = "succeeded"
		return &r, save(v, &r)
	}
	if e = page.guard(&r, plan, true); e != nil {
		return &r, e
	}
	if !pay {
		return &r, nil
	}
	c, e := readCard(v)
	if e != nil {
		return &r, e
	}
	method, e := s.tokenize(ctx, &r, c)
	if e != nil {
		return &r, e
	}
	// Refresh after card creation; confirmation also pins the expected amount server-side.
	page, e = s.page(ctx, &r, true)
	if e != nil {
		return &r, e
	}
	if e = page.guard(&r, plan, true); e != nil {
		return &r, e
	}
	r.Status = "submitting"
	if e = save(v, &r); e != nil {
		return &r, e
	}
	_, confirmErr := s.confirm(ctx, &r, page, method, plan)
	// Never issue confirm twice, even if the response is lost or a validation error occurs.
	for i := 0; i < 6; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				break
			case <-time.After(2 * time.Second):
			}
		}
		current, readErr := s.page(ctx, &r, true)
		if readErr == nil && current.guard(&r, plan, false) == nil {
			if current.Status == "complete" && current.PaymentStatus == "paid" {
				r.Status = "succeeded"
				return &r, save(v, &r)
			}
			if current.Intent != nil && current.Intent.Status == "requires_action" {
				r.Status = "requires_action"
				if e = save(v, &r); e != nil {
					return &r, e
				}
				return &r, errors.New("bank authentication is required; use this existing checkout")
			}
		}
		if confirmErr != nil {
			break
		}
	}
	r.Status = "unknown"
	if e = save(v, &r); e != nil {
		return &r, e
	}
	if confirmErr != nil {
		return &r, confirmErr
	}
	return &r, errors.New("Stripe has not confirmed payment; automatic retry is blocked")
}
