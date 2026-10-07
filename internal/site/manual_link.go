package site

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
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

func (s *server) publicLinkPlans(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("__Host-xgift-link"); err != nil || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(c.Value) {
		var b [32]byte
		if _, err = rand.Read(b[:]); err != nil {
			message(w, 503, "请稍后重试。")
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "__Host-xgift-link", Value: hex.EncodeToString(b[:]), Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 30 * 24 * 60 * 60})
	}
	s.manualLinkPlans(w, r)
}
func (s *server) publicLink(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie("__Host-xgift-link")
	if err != nil || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(c.Value) {
		message(w, 400, "请刷新页面后重新生成链接。")
		return
	}
	s.generateManualLink(w, r, c.Value)
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
	q.Username = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(q.Username), "@"))
	if !usernamePattern.MatchString(q.Username) || q.Months < 1 || q.Months > 24 {
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
		if s.tryServePublicLink(w, r, q, publicOwner, true) {
			return
		}
		s.enqueuePublicLink(w, q, publicOwner)
		return
	}
	s.executeManualLink(w, r, q, publicOwner)
}

func (s *server) executeManualLink(w http.ResponseWriter, r *http.Request, q manualLinkRequest, publicOwner string) {
	select {
	case s.work <- struct{}{}:
	default:
		w.Header().Set("Retry-After", "3")
		message(w, 409, "有订单正在处理，请稍后再生成链接。")
		return
	}
	defer func() { <-s.work }()
	lock, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		w.Header().Set("Retry-After", "3")
		message(w, 503, "无法锁定订单，请稍后重试。")
		return
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		w.Header().Set("Retry-After", "3")
		message(w, 409, "有订单正在处理，请稍后再生成链接。")
		return
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	ctx, cancel := context.WithTimeout(r.Context(), 110*time.Second)
	defer cancel()
	var record *checkout.Record
	if publicOwner != "" {
		record, err = checkout.PublicLinkForUsername(ctx, s.vault, q.Username, publicOwner, s.port, q.Months, q.VerifiedUnpaid)
	} else {
		record, err = checkout.ManualLinkForUsername(ctx, s.vault, q.Username, s.port, q.Months, q.VerifiedUnpaid)
	}
	if err != nil {
		// Error classification only: never log links, ownership cookies, card data,
		// upstream response bodies or authorization headers.
		// Waiting for the active payment window is normal at the queue head.
		reason := manualLinkFailureReason(err)
		if !errors.Is(err, checkout.ErrCheckoutRateLimited) {
			log.Printf("manual link failed: public=%t username=%s months=%d reason=%s", publicOwner != "", q.Username, q.Months, reason)
		}
		switch {
		case errors.Is(err, checkout.ErrCheckoutRateLimited):
			seconds := 15
			var wait *checkout.CheckoutWaitError
			if errors.As(err, &wait) {
				seconds = int((wait.Wait + time.Second - 1) / time.Second)
				if seconds < 1 {
					seconds = 1
				}
				w.Header().Set("X-Checkout-Wait-Seconds", strconv.Itoa(seconds))
			}
			// Recheck payment completion while retaining the real window for ETA.
			retry := seconds
			if retry > 10 {
				retry = 10
			}
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			message(w, 429, "正在等待处理，请稍候。")
		case errors.Is(err, checkout.ErrPublicPaymentDeclined):
			w.Header().Set("X-Checkout-Requeue", "declined")
			message(w, 409, "上游拒绝了本次付款，通道已让给下一位。重试需要重新排队。")
		case errors.Is(err, checkout.ErrPublicPaymentInProgress):
			message(w, 409, "该订单正在付款或银行验证中，请先完成当前付款。")
		case errors.Is(err, checkout.ErrPublicLinkPrivateOrder):
			message(w, 409, "该账号已有兑换或后台订单，请使用原付款链接或联系管理员；主页不会重复创建订单。")
		case errors.Is(err, checkout.ErrPublicLinkPending):
			message(w, 409, "暂未取得付款链接，系统没有提交付款。请用相同账号和套餐重试；请勿同时使用其他入口重复建单。")
		case errors.Is(err, checkout.ErrPublicLinkConflict):
			message(w, 409, "该账号暂时无法生成新链接，请使用原付款页面或联系管理员核实。")
		case errors.Is(err, checkout.ErrVerifyUnpaid):
			if publicOwner != "" {
				message(w, 409, "原付款链接已失效且未完成付款，请重新排队获取新链接。")
			} else {
				reply(w, 409, map[string]any{"message": "原付款链接已失效，请核实原订单未付款后再重新生成。", "needs_unpaid_verification": true})
			}
		case errors.Is(err, checkout.ErrNotEligible):
			message(w, 409, "X 目前不允许 @"+q.Username+" 接收 Premium 赠送，本次没有建单或扣款。可先用本页「检测赠送资格」确认，或改为其他账号。")
		case errors.Is(err, checkout.ErrUserNotFound):
			message(w, 404, "未找到 X 用户名 @"+q.Username+"。请填写个人主页 @ 后面的用户名（不是显示名称），核对拼写后重新提交；本次没有建单或扣款。")
		case errors.Is(err, checkout.ErrManualLinkConflict):
			message(w, 409, "该客户已有其他套餐或账号信息的订单，请先通过客户查询核实原订单。")
		default:
			if record != nil && record.SubmittedAt != 0 {
				message(w, 409, "原付款尚未确认可以重建，请先核实原付款结果。")
			} else {
				message(w, 502, "暂时无法生成付款链接，请稍后重试；重复请求会优先检查已有订单。")
			}
		}
		return
	}
	s.respondManualLink(w, record, publicOwner)
}

func (s *server) respondManualLink(w http.ResponseWriter, record *checkout.Record, publicOwner string) {
	if record == nil {
		message(w, 502, "未取得有效订单。")
		return
	}
	result := map[string]any{"username": record.Username, "months": record.Months, "amount": record.Amount, "currency": record.Currency, "status": record.Status}
	if record.Status == "succeeded" {
		result["message"] = "该客户的这笔订单已付款成功，无需再次付款。"
	} else {
		link := checkout.CheckoutLink(record)
		if link == "" {
			message(w, 502, "未取得有效付款链接。")
			return
		}
		result["checkout_url"] = link
		if publicOwner != "" {
			result["expires_at"] = record.Created + int64(checkout.PublicLinkTTL/time.Second)
			s.invalidateOlderPublicResults(record.Username, link)
			log.Printf("public link ready: username=%s months=%d stripe_verified=true", record.Username, record.Months)
		}
	}
	reply(w, 200, result)
}

func manualLinkFailureReason(err error) string {
	switch {
	case errors.Is(err, checkout.ErrCheckoutRateLimited):
		return "creation_rate_limited"
	case errors.Is(err, checkout.ErrPublicPaymentInProgress):
		return "payment_in_progress"
	case errors.Is(err, checkout.ErrPublicLinkPrivateOrder):
		return "private_order"
	case errors.Is(err, checkout.ErrPublicLinkPending):
		return "creation_pending"
	case errors.Is(err, checkout.ErrPublicLinkConflict):
		return "public_order_conflict"
	case errors.Is(err, checkout.ErrVerifyUnpaid):
		return "requires_unpaid_confirmation"
	case errors.Is(err, checkout.ErrNotEligible):
		return "ineligible"
	case errors.Is(err, checkout.ErrUserNotFound):
		return "user_not_found"
	case errors.Is(err, checkout.ErrXReadFailure):
		return "x_read_failure"
	default:
		return "upstream_or_order_verification"
	}
}

// The current slot holder may retrieve its link or replace its own plan.
// Both operations retain the work/file locks and the global creation guard.
func (s *server) tryServePublicLink(w http.ResponseWriter, r *http.Request, q manualLinkRequest, owner string, allowReplacement bool) bool {
	if s.vault == nil {
		return false
	}
	user, months, _, err := checkout.PublicCheckoutWindow(s.vault, time.Now())
	if err != nil || user != q.Username || (!allowReplacement && months != q.Months) {
		return false
	}
	waitForRead := func() bool {
		ticket := r.PathValue("ticket")
		if allowReplacement || ticket == "" {
			return false
		}
		reply(w, 202, map[string]any{"ticket": ticket, "status": "queued", "ahead": 0, "estimated_wait_seconds": 5, "message": "正在核验付款链接，请稍候。"})
		return true
	}
	select {
	case s.work <- struct{}{}:
	default:
		return waitForRead()
	}
	defer func() { <-s.work }()
	lock, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return false
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return waitForRead()
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	timeout := 20 * time.Second
	if allowReplacement && months != q.Months {
		timeout = 110 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
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
	s.respondManualLink(w, record, owner)
	return true
}
