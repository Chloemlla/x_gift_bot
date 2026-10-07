package site

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"xgift/internal/checkout"
)

func (s *server) manualLinkPlans(w http.ResponseWriter, r *http.Request) {
	cat, err := checkout.ReadCatalog(s.vault)
	if err != nil {
		message(w, 503, "套餐配置暂不可用。")
		return
	}
	type plan struct {
		Months   int    `json:"months"`
		Amount   int    `json:"amount"`
		Currency string `json:"currency"`
	}
	plans := make([]plan, 0, len(cat.Plans))
	for _, p := range cat.Plans {
		plans = append(plans, plan{p.Months, p.Amount, strings.ToUpper(cat.Currency)})
	}
	reply(w, 200, map[string]any{"plans": plans})
}

// linkOwner returns this browser's public-link cookie, issuing one if needed.
func linkOwner(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie("__Host-xgift-link"); err == nil && checkout.ValidOwner(c.Value) {
		return c.Value
	}
	owner := token(32)
	http.SetCookie(w, &http.Cookie{Name: "__Host-xgift-link", Value: owner, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 30 * 24 * 60 * 60})
	return owner
}

func (s *server) publicLinkPlans(w http.ResponseWriter, r *http.Request) {
	linkOwner(w, r)
	s.manualLinkPlans(w, r)
}
func (s *server) publicLink(w http.ResponseWriter, r *http.Request) {
	s.generateManualLink(w, r, linkOwner(w, r))
}
func (s *server) manualLink(w http.ResponseWriter, r *http.Request) {
	s.generateManualLink(w, r, "")
}

type manualLinkRequest struct {
	Username       string `json:"username"`
	Months         int    `json:"months"`
	VerifiedUnpaid bool   `json:"verified_unpaid"`
}

func (s *server) generateManualLink(w http.ResponseWriter, r *http.Request, publicOwner string) {
	var q manualLinkRequest
	if !decode(w, r, &q) {
		return
	}
	var ok bool
	if q.Username, ok = checkout.NormalizeUsername(q.Username); !ok || q.Months < 1 || q.Months > 24 {
		message(w, 400, "请填写正确的 X 用户名并选择套餐时长。")
		return
	}
	cat, err := checkout.ReadCatalog(s.vault)
	if err != nil {
		message(w, 503, "套餐配置暂不可用。")
		return
	}
	if _, err = cat.PlanFor(q.Months); err != nil {
		message(w, 400, "该套餐时长未配置。")
		return
	}
	if publicOwner != "" {
		if s.tryServePublicLink(w, r, q, publicOwner) {
			return
		}
		s.enqueuePublicLink(w, q, publicOwner)
		return
	}
	s.createLink(r.Context(), q, "").write(w)
}

// linkOutcome is the result of one link attempt. retryIn > 0 marks a transient
// wait (the queue keeps the ticket); blocked is the creation window for ETAs.
type linkOutcome struct {
	status   int
	body     map[string]any
	retryIn  time.Duration
	blocked  time.Duration
	declined bool
}

func failed(status int, msg string) linkOutcome {
	return linkOutcome{status: status, body: map[string]any{"message": msg}}
}

func (o linkOutcome) write(w http.ResponseWriter) {
	if o.retryIn > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int((o.retryIn+time.Second-1)/time.Second)))
	}
	reply(w, o.status, o.body)
}

func (s *server) createLink(ctx context.Context, q manualLinkRequest, publicOwner string) linkOutcome {
	release, ok := s.tryLock()
	if !ok {
		o := failed(409, "有订单正在处理，请稍后再生成链接。")
		o.retryIn = 3 * time.Second
		return o
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, 110*time.Second)
	defer cancel()
	var record *checkout.Record
	var err error
	if publicOwner != "" {
		record, err = checkout.PublicLinkForUsername(ctx, s.vault, q.Username, publicOwner, s.port, q.Months)
	} else {
		record, err = checkout.ManualLinkForUsername(ctx, s.vault, q.Username, s.port, q.Months, q.VerifiedUnpaid)
	}
	if err == nil {
		return s.linkResult(record, publicOwner)
	}
	// Error classification only: never log links, ownership cookies, card data,
	// upstream response bodies or authorization headers.
	var o linkOutcome
	reason := ""
	switch {
	case errors.Is(err, checkout.ErrCheckoutRateLimited):
		// Waiting for the active payment window is normal at the queue head.
		// Recheck early for completed payments while keeping the real ETA.
		o = failed(429, "正在等待处理，请稍候。")
		o.blocked = 15 * time.Second
		var wait *checkout.CheckoutWaitError
		if errors.As(err, &wait) {
			o.blocked = max(wait.Wait, time.Second)
		}
		o.retryIn = min(o.blocked, 10*time.Second)
		return o
	case errors.Is(err, checkout.ErrPublicPaymentDeclined):
		reason, o = "payment_declined", failed(409, "上游拒绝了本次付款，通道已让给下一位。重试需要重新排队。")
		o.declined = true
	case errors.Is(err, checkout.ErrPublicPaymentInProgress):
		reason, o = "payment_in_progress", failed(409, "该订单正在付款或银行验证中，请先完成当前付款。")
	case errors.Is(err, checkout.ErrPublicLinkPrivateOrder):
		reason, o = "private_order", failed(409, "该账号已有兑换或后台订单，请使用原付款链接或联系管理员；主页不会重复创建订单。")
	case errors.Is(err, checkout.ErrPublicLinkPending):
		reason, o = "creation_pending", failed(409, "暂未取得付款链接，系统没有提交付款。请用相同账号和套餐重试；请勿同时使用其他入口重复建单。")
	case errors.Is(err, checkout.ErrPublicLinkConflict):
		reason, o = "public_order_conflict", failed(409, "该账号暂时无法生成新链接，请使用原付款页面或联系管理员核实。")
	case errors.Is(err, checkout.ErrVerifyUnpaid) && publicOwner != "":
		reason, o = "link_expired_unpaid", failed(409, "原付款链接已失效且未完成付款，请重新排队获取新链接。")
	case errors.Is(err, checkout.ErrVerifyUnpaid):
		reason, o = "requires_unpaid_confirmation", linkOutcome{status: 409, body: map[string]any{"message": "原付款链接已失效，请核实原订单未付款后再重新生成。", "needs_unpaid_verification": true}}
	case errors.Is(err, checkout.ErrNotEligible):
		reason, o = "ineligible", failed(409, "X 目前不允许 @"+q.Username+" 接收 Premium 赠送，本次没有建单或扣款。可先用本页「检测赠送资格」确认，或改为其他账号。")
	case errors.Is(err, checkout.ErrUserNotFound):
		reason, o = "user_not_found", failed(404, "未找到 X 用户名 @"+q.Username+"。请填写个人主页 @ 后面的用户名（不是显示名称），核对拼写后重新提交；本次没有建单或扣款。")
	case errors.Is(err, checkout.ErrManualLinkConflict):
		reason, o = "manual_link_conflict", failed(409, "该客户已有其他套餐或账号信息的订单，请先通过客户查询核实原订单。")
	case record != nil && record.SubmittedAt != 0:
		reason, o = "submitted_order", failed(409, "原付款尚未确认可以重建，请先核实原付款结果。")
	default:
		reason, o = "upstream_or_order_verification", failed(502, "暂时无法生成付款链接，请稍后重试；重复请求会优先检查已有订单。")
		if errors.Is(err, checkout.ErrXReadFailure) {
			reason = "x_read_failure"
		}
	}
	log.Printf("manual link failed: public=%t username=%s months=%d reason=%s", publicOwner != "", q.Username, q.Months, reason)
	return o
}

func (s *server) linkResult(record *checkout.Record, publicOwner string) linkOutcome {
	result := map[string]any{"username": record.Username, "months": record.Months, "amount": record.Amount, "currency": record.Currency, "status": record.Status}
	if record.Status == "succeeded" {
		result["message"] = "该客户的这笔订单已付款成功，无需再次付款。"
		return linkOutcome{status: 200, body: result}
	}
	link := checkout.CheckoutLink(record)
	if link == "" {
		return failed(502, "未取得有效付款链接。")
	}
	result["checkout_url"] = link
	if publicOwner != "" {
		result["expires_at"] = record.Created + int64(checkout.PublicLinkTTL/time.Second)
		s.invalidateOlderPublicResults(record.Username, link)
		log.Printf("public link ready: username=%s months=%d stripe_verified=true", record.Username, record.Months)
	}
	return linkOutcome{status: 200, body: result}
}

// The current payment-window holder may retrieve its link or replace its own
// plan without queueing behind others. Busy locks fall back to the queue.
func (s *server) tryServePublicLink(w http.ResponseWriter, r *http.Request, q manualLinkRequest, owner string) bool {
	user, months, _, err := checkout.PublicCheckoutWindow(s.vault, time.Now())
	if err != nil || user != q.Username {
		return false
	}
	release, ok := s.tryLock()
	if !ok {
		return false
	}
	defer release()
	ctx, cancel := context.WithTimeout(r.Context(), 110*time.Second)
	defer cancel()
	var record *checkout.Record
	hit := true
	if months != q.Months {
		record, err = checkout.PublicLinkForUsername(ctx, s.vault, q.Username, owner, s.port, q.Months)
	} else {
		record, hit, err = checkout.TryCachedPublicLink(ctx, s.vault, q.Username, owner, s.port, q.Months)
	}
	if err != nil || !hit {
		return false
	}
	s.linkResult(record, owner).write(w)
	return true
}
