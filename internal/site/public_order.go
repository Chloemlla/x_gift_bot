package site

import (
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"
	"xgift/internal/checkout"
)

// Read-only lookup of the latest public order created by this browser. It does
// not join the queue, create orders, or reveal links, cards or other browsers'
// orders; only coarse status, plan and time are returned.
func (s *server) publicOrderStatus(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie("__Host-xgift-link")
	if err != nil || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(c.Value) {
		message(w, 404, "本浏览器没有付款记录。请使用生成付款链接时的同一浏览器查询。")
		return
	}
	user := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(r.URL.Query().Get("username")), "@"))
	if !usernamePattern.MatchString(user) {
		message(w, 400, "请填写正确的 X 用户名。")
		return
	}
	if s.vault == nil {
		message(w, 503, "暂时无法查询，请稍后重试。")
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
	now := time.Now()
	state := "ended"
	result := map[string]any{"username": order.Username, "months": order.Months, "created": order.Created}
	switch {
	case order.Status == "succeeded":
		state = "paid"
	case order.Status == "creating":
		state = "not_created"
	case checkout.PublicOrderOpen(order, now):
		state = "open"
		result["expires_at"] = order.Created + int64(checkout.PublicLinkTTL/time.Second)
	}
	result["state"] = state
	reply(w, 200, result)
}
