package site

import (
	"context"
	"errors"
	"net/http"
	"os"
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

func (s *server) manualLink(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Username       string `json:"username"`
		Months         int    `json:"months"`
		VerifiedUnpaid bool   `json:"verified_unpaid"`
	}
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
	select {
	case s.work <- struct{}{}:
	default:
		message(w, 409, "有订单正在处理，请稍后再生成链接。")
		return
	}
	defer func() { <-s.work }()
	lock, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		message(w, 503, "无法锁定订单，请稍后重试。")
		return
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		message(w, 409, "有订单正在处理，请稍后再生成链接。")
		return
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	ctx, cancel := context.WithTimeout(r.Context(), 110*time.Second)
	defer cancel()
	record, err := checkout.ManualLinkForUsername(ctx, s.vault, q.Username, s.port, q.Months, q.VerifiedUnpaid)
	if err != nil {
		switch {
		case errors.Is(err, checkout.ErrVerifyUnpaid):
			reply(w, 409, map[string]any{"message": "原付款链接已失效，请核实原订单未付款后再重新生成。", "needs_unpaid_verification": true})
		case errors.Is(err, checkout.ErrNotEligible):
			message(w, 409, "该账号目前无法接收 Premium 赠送。")
		case errors.Is(err, checkout.ErrUserNotFound):
			message(w, 404, "未找到该 X 用户名。")
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
	}
	reply(w, 200, result)
}
