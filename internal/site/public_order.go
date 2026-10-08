package site

import (
	"context"
	"log"
	"net/http"
	"time"
	"xgift/internal/checkout"
)

// Lookup of the latest public order created by this browser. It only reads
// Stripe and records a confirmed payment; it never joins the queue, creates
// orders, or reveals links, cards or other browsers' orders.
func (s *server) publicOrderStatus(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie("__Host-xgift-link")
	if err != nil || !checkout.ValidOwner(c.Value) {
		message(w, 404, "本浏览器没有付款记录。请使用生成付款链接时的同一浏览器查询。")
		return
	}
	user, ok := checkout.NormalizeUsername(r.URL.Query().Get("username"))
	if !ok {
		message(w, 400, "请填写正确的 X 用户名。")
		return
	}
	order, err := checkout.PublicOrderStatus(s.vault, user, c.Value)
	if err != nil {
		log.Printf("public order lookup failed")
		message(w, 503, "暂时无法查询，请稍后重试。")
		return
	}
	if order == nil {
		message(w, 404, "本浏览器没有找到 @"+user+" 的付款记录。查询只包含在本浏览器生成的付款链接；在其他设备付款的，请在原设备上查询。")
		return
	}
	// Confirm payment with Stripe (read-only) so a paid but unrecorded order is
	// not shown as unpaid. Persist only when the order lock is free.
	stripeChecked := false
	if order.Status != "succeeded" {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		paid, err := checkout.PublicOrderPaid(ctx, s.vault, order)
		cancel()
		stripeChecked = err == nil
		if paid {
			order.Status = "succeeded"
			if release, ok := s.tryLock(); ok {
				if err := checkout.RecordPublicOrderPaid(s.vault, order); err != nil {
					log.Printf("public order payment could not be recorded")
				}
				release()
			}
		}
	}
	now := time.Now()
	state := "ended"
	result := map[string]any{"username": order.Username, "months": order.Months, "created": order.Created}
	switch {
	case order.Status == "succeeded":
		state = "paid"
	case !stripeChecked || order.Status == "creating":
		state = "unknown"
	case checkout.PublicOrderOpen(order, now):
		state = "open"
		result["expires_at"] = order.Created + int64(checkout.PublicLinkTTL/time.Second)
	}
	result["state"] = state
	result["stripe_checked"] = stripeChecked
	reply(w, 200, result)
}
