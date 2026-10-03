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

// CardSummary exposes only a display suffix and an opaque configuration binding.
func CardSummary(v *vault.Vault) (last4, binding string, err error) {
	c, err := readCard(v)
	if err != nil {
		return "", "", err
	}
	binding, err = paymentCardFingerprint(v)
	return c.Number[len(c.Number)-4:], binding, err
}

// ManualRetryBlocked deliberately treats bank instructions as a stop condition.
func ManualRetryBlocked(r *Record) bool {
	if r == nil || r.LastError == nil {
		return false
	}
	e := r.LastError
	if e.AdviceCode != "" && e.AdviceCode != "try_again_later" {
		return true
	}
	if e.NetworkAdviceCode != "" {
		return true
	}
	switch e.DeclineCode {
	case "fraudulent", "lost_card", "stolen_card", "pickup_card", "restricted_card", "revocation_of_authorization", "revocation_of_all_authorizations", "stop_payment_order", "transaction_not_allowed", "card_not_supported", "expired_card", "incorrect_number", "incorrect_cvc", "invalid_cvc":
		return true
	}
	return false
}

type manualProof struct {
	Page   json.RawMessage       `json:"page"`
	Intent *stripeIntentEvidence `json:"intent"`
}

func manualProofKey(r *Record) string {
	return fmt.Sprintf("manual-preflight:%s:%d", r.SessionID, r.RecoveryAttempts)
}
func (p *manualProof) guard(r *Record, plan Plan) error {
	var page paymentPage
	if err := json.Unmarshal(p.Page, &page); err != nil {
		return err
	}
	if err := page.guard(r, plan, false); err != nil {
		return err
	}
	if page.Status != "open" || page.PaymentStatus != "unpaid" || page.Checksum == "" || page.Total.Due != plan.Minor || page.Group.Due != plan.Minor {
		return errors.New("existing checkout is not open and unpaid")
	}
	i := p.Intent
	if i == nil {
		return page.guard(r, plan, true)
	}
	if page.Intent == nil || page.Intent.ID != i.ID || !regexp.MustCompile(`^pi_[A-Za-z0-9]+$`).MatchString(i.ID) || !i.Live || i.Status != "requires_payment_method" || i.Amount != plan.Minor || i.Currency != plan.Currency || i.Received == nil || *i.Received != 0 || i.Capturable == nil || *i.Capturable != 0 {
		return errors.New("existing payment lacks conclusive unpaid retry evidence")
	}
	return nil
}
func (s *stripeClient) manualPreflight(ctx context.Context, r *Record, plan Plan) (*paymentPage, *manualProof, error) {
	page, err := s.page(ctx, r, true)
	if err != nil {
		return nil, nil, err
	}
	if err = page.guard(r, plan, false); err != nil {
		return nil, nil, err
	}
	var envelope struct {
		Intent *stripeIntentEvidence `json:"payment_intent"`
	}
	if err = json.Unmarshal(page.raw, &envelope); err != nil {
		return nil, nil, err
	}
	i := envelope.Intent
	if i == nil && page.IntentNull {
		proof := &manualProof{Page: page.raw}
		if err = proof.guard(r, plan); err != nil {
			return nil, nil, err
		}
		return page, proof, nil
	}
	if i == nil || !regexp.MustCompile(`^pi_[A-Za-z0-9]+$`).MatchString(i.ID) || !strings.HasPrefix(i.ClientSecret, i.ID+"_secret_") {
		return nil, nil, errors.New("existing checkout has no verifiable linked payment intent")
	}
	proof := &manualProof{Page: page.raw}
	if err = s.call(ctx, "GET", "payment_intents/"+i.ID, url.Values{"client_secret": {i.ClientSecret}}, "", &proof.Intent); err != nil {
		return nil, nil, err
	}
	if err = proof.guard(r, plan); err != nil {
		return nil, nil, err
	}
	return page, proof, nil
}

// ManualRecoverForRecipient is available only to the explicit admin batch action.
// Caller holds checkout.lock. It NEVER replaces a submitted session with a new one.
func ManualRecoverForRecipient(ctx context.Context, v *vault.Vault, user, recipient string, port, months int) (*Record, error) {
	raw, err := v.Get("checkout:" + recipient)
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
	if r.Username != user || r.RecipientID != recipient || r.Months != months || r.Amount != plan.Minor || r.Currency != strings.ToUpper(plan.Currency) || r.ProductID != plan.ProductID || !sessionURL(r.URL, r.SessionID) {
		return nil, errors.New("bound order identity or price mismatch")
	}
	if r.Status == "succeeded" {
		return &r, nil
	}
	if !IsPaymentDeclined(&r) {
		return ResumeForRecipient(ctx, v, user, recipient, port, months)
	}
	if ManualRetryBlocked(&r) {
		return &r, errors.New("payment provider forbids retry with this card")
	}
	if paused, e := PaymentPaused(v); e != nil || paused {
		if e != nil {
			return &r, e
		}
		return &r, ErrPaymentPaused
	}
	if err = verifySubmission(v, &r, plan); err != nil {
		return &r, err
	}
	s, err := newStripe(ctx, v, r.RecipientID)
	if err != nil {
		return &r, err
	}
	defer s.close()
	return manualRecoverDeclined(ctx, v, &r, s, plan, func() error {
		x, e := newXClient(v, port)
		if e != nil {
			return e
		}
		defer x.close()
		id, e := x.recipient(ctx, user)
		if e != nil {
			return e
		}
		if id != recipient {
			return errors.New("recipient changed")
		}
		return x.quote(ctx, user, plan)
	})
}
func manualRecoverDeclined(ctx context.Context, v *vault.Vault, r *Record, s *stripeClient, plan Plan, eligibility func() error) (*Record, error) {
	state, err := s.poll(ctx, r, plan)
	if err != nil {
		return r, err
	}
	if state == "succeeded" {
		r.Status = "succeeded"
		r.LastError = nil
		return r, save(v, r)
	}
	if state == "requires_action" {
		r.Status = state
		if err = save(v, r); err != nil {
			return r, err
		}
		return r, errors.New("bank authentication required")
	}
	if err = eligibility(); err != nil {
		return r, err
	}
	_, _, err = s.manualPreflight(ctx, r, plan)
	if err != nil {
		return r, err
	}
	old, err := json.Marshal(r)
	if err != nil {
		return r, err
	}
	defer clear(old)
	next := *r
	next.ManualRecovery = true
	next.RecoveryAttempts++
	if next.RecoveryAttempts > 3 {
		return r, errors.New("manual retry limit reached; inspect payment method")
	}
	if err = v.Put(fmt.Sprintf("manual-previous:%s:%d", r.SessionID, next.RecoveryAttempts), old); err != nil {
		return r, err
	}
	c, err := readCard(v)
	if err != nil {
		return r, err
	}
	method, err := s.tokenize(ctx, &next, c)
	if err != nil {
		return r, err
	}
	page, proof, err := s.manualPreflight(ctx, r, plan)
	if err != nil {
		return r, err
	}
	if err = reservePaymentSlot(ctx, v, &next); err != nil {
		return r, err
	}
	b, err := json.Marshal(proof)
	if err != nil {
		return r, err
	}
	defer clear(b)
	if err = v.Put(manualProofKey(&next), b); err != nil {
		return r, err
	}
	next.PreflightSaved = true
	next.PaymentMethod = method
	next.ConfirmParameters = confirmationForm(&next, page, method, plan).Encode()
	next.ConfirmKey = idempotency(&next, "confirm")
	next.SubmittedAt = time.Now().Unix()
	next.Status = "submitting"
	return confirmAndObserve(ctx, v, &next, s, plan)
}

// A hard no-retry instruction cannot be cleared by the batch confirmation.
func ResetManualPaymentPause(v *vault.Vault) error {
	state, err := readPaymentControl(v)
	if err != nil {
		return err
	}
	if state.Reason == "do_not_try_again" {
		return errors.New("payment provider forbids retry with this card")
	}
	return ResetPaymentPause(v)
}

// Only fixed messages are returned to the dashboard; raw errors stay encrypted.
func ManualRecoveryErrorMessage(err error) string {
	if err == nil {
		return "付款尚未确认，已停止；请查询原订单。"
	}
	text := err.Error()
	switch {
	case errors.Is(err, ErrPaymentNodesCooling):
		return "付款节点暂不可用，已停止自动尝试；请查看冷却状态并核实原订单结果。"
	case errors.Is(err, ErrVerifyUnpaid):
		return replacementMessage(err)
	case errors.Is(err, ErrPaymentPaused):
		return "银行卡付款保护已暂停，未继续提交。"
	case strings.Contains(text, "checkout_not_active_session"):
		return "原账单已失效，未提交补单付款；需要单独核实。"
	case strings.Contains(text, "unpaid"), strings.Contains(text, "payment intent"), strings.Contains(text, "payment_intent"):
		return "无法确认原账单可安全重试，未提交补单付款；请检查原账单。"
	case strings.Contains(text, "manual retry limit"):
		return "人工重试次数已达上限，请检查付款方式。"
	case strings.Contains(text, "mismatch"), strings.Contains(text, "changed"):
		return "原订单身份、金额或付款凭据不一致，未提交补单付款。"
	default:
		return "补单未确认完成；详细错误已保存到服务器加密诊断记录。"
	}
}
