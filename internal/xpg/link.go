package xpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"xgift/internal/checkout"
)

const (
	linkJobTimeout = 6 * time.Minute
	checksPerMin   = 15
	linksPerHour   = 30
	beatInterval   = 15 * time.Second
)

// plans exposes the operator-configured gift plans. Amounts and product IDs
// are public X checkout data.
func (s *server) plans(w http.ResponseWriter, r *http.Request) {
	cat, err := checkout.ReadCatalog(s.vault)
	if err != nil {
		replyError(w, 503, "not_ready", "服务目录未配置，请稍后再试")
		return
	}
	out := make([]map[string]any, 0, len(cat.Plans))
	for _, p := range cat.Plans {
		out = append(out, map[string]any{
			"months":      p.Months,
			"amount":      p.Amount,
			"currency":    strings.ToUpper(cat.Currency),
			"product_id":  p.Product,
			"merchant_id": cat.Merchant,
		})
	}
	reply(w, 200, map[string]any{"plans": out})
}

// check verifies that the username exists and may receive a Premium gift.
// This is the explicit read-only first step required before a link is made.
func (s *server) check(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		replyError(w, 401, "unauthorized", "访问密钥不正确")
		return
	}
	if !s.limiter.allow("check:"+clientIP(r), time.Minute, checksPerMin) {
		replyError(w, 429, "rate_limited", "检测过于频繁，请稍后再试")
		return
	}
	var req struct{ Username string }
	if !decodeBody(w, r, &req) {
		return
	}
	user, ok := checkout.NormalizeUsername(req.Username)
	if !ok {
		replyError(w, 400, "invalid_username", "用户名格式不正确（1-15 位字母、数字或下划线）")
		return
	}
	id, err := checkout.Eligibility(r.Context(), s.vault, user, s.proxyPort)
	if err != nil {
		status, code, message := describeCheckError(err)
		replyError(w, status, code, message)
		return
	}
	reply(w, 200, map[string]any{"username": user, "id": id, "eligible": true})
}

func describeCheckError(err error) (status int, code, message string) {
	switch {
	case errors.Is(err, checkout.ErrUserNotFound):
		return 404, "user_not_found", "未找到该 X 账号，请确认用户名拼写"
	case errors.Is(err, checkout.ErrNotEligible):
		return 422, "not_eligible", "该账号目前无法接收 Premium 赠品（可能已订阅、收过近期赠品或账号受限）"
	case errors.Is(err, checkout.ErrXReadFailure):
		return 502, "x_read_failed", "X 账号或资格查询失败，请稍后重试"
	default:
		text := err.Error()
		if strings.Contains(text, "X API authentication") || strings.Contains(text, "X cookie") ||
			strings.Contains(text, "catalog record is missing") || strings.Contains(text, "proxy") {
			return 503, "not_ready", "服务凭据或配置未就绪，请联系管理员"
		}
		return 502, "check_failed", "资格检测失败，请稍后重试"
	}
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		replyError(w, 400, "invalid_request", "请求体不符合要求")
		return false
	}
	return true
}

// emitter writes one JSON object per line and flushes. A quiet heartbeat keeps
// reverse proxies and Cloudflare from closing an idle waiting stream.
type emitter struct {
	mu    sync.Mutex
	enc   *json.Encoder
	flush func()
	stop  chan struct{}
	done  sync.WaitGroup
}

func newEmitter(w http.ResponseWriter, flusher http.Flusher) *emitter {
	e := &emitter{enc: json.NewEncoder(w), flush: flusher.Flush, stop: make(chan struct{})}
	e.done.Add(1)
	go func() {
		defer e.done.Done()
		ticker := time.NewTicker(beatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-e.stop:
				return
			case <-ticker.C:
				e.event(map[string]any{"t": "beat"})
			}
		}
	}()
	return e
}

func (e *emitter) event(v any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.enc.Encode(v); err != nil {
		return
	}
	e.flush()
}

func (e *emitter) close() {
	close(e.stop)
	e.done.Wait()
}

// link generates a Stripe checkout URL after a passed check. Requests are
// serialized: one creation at a time, streaming NDJSON progress lines.
func (s *server) link(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		replyError(w, 401, "unauthorized", "访问密钥不正确")
		return
	}
	if !s.limiter.allow("link:"+clientIP(r), time.Hour, linksPerHour) {
		replyError(w, 429, "rate_limited", "生成过于频繁，请稍后再试")
		return
	}
	var req struct {
		Username string `json:"username"`
		Months   int    `json:"months"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	user, ok := checkout.NormalizeUsername(req.Username)
	if !ok {
		replyError(w, 400, "invalid_username", "用户名格式不正确（1-15 位字母、数字或下划线）")
		return
	}
	cat, err := checkout.ReadCatalog(s.vault)
	if err != nil {
		replyError(w, 503, "not_ready", "服务目录未配置，请稍后再试")
		return
	}
	plan, err := cat.PlanFor(req.Months)
	if err != nil {
		replyError(w, 400, "unsupported_plan", "不支持的礼品时长")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		replyError(w, 500, "unsupported", "当前服务器不支持流式响应")
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if rc := http.NewResponseController(w); rc != nil {
		_ = rc.SetWriteDeadline(time.Time{})
	}

	emit := newEmitter(w, flusher)
	defer emit.close()

	owner := requestOwner(w, r)
	if owner == "" {
		emit.event(errorEvent("internal", "无法标识浏览器，请重试", ""))
		return
	}
	emit.event(map[string]any{"t": "start", "username": user, "months": plan.Months})

	select {
	case s.job <- struct{}{}:
	case <-r.Context().Done():
		return
	default:
		emit.event(progressEvent(-1, "已有链接正在生成，当前请求排队等待…"))
		select {
		case s.job <- struct{}{}:
		case <-r.Context().Done():
			return
		}
	}
	defer func() { <-s.job }()

	jobCtx, cancel := context.WithTimeout(r.Context(), linkJobTimeout)
	defer cancel()
	jctx := checkout.WithProgress(jobCtx, func(percent int, message string) {
		emit.event(progressEvent(percent, message))
	})
	rec, err := s.createLink(jctx, user, owner, plan.Months, func(percent int, message string) {
		emit.event(progressEvent(percent, message))
	})
	if err != nil {
		code, message := describeLinkError(err)
		detail := err.Error()
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			code, message, detail = "canceled", "生成已取消或超时，可重新生成", ""
		}
		emit.event(errorEvent(code, message, detail))
		return
	}
	emit.event(resultEvent(rec))
	// A final short wait keeps the beat goroutine from racing the last line.
	time.Sleep(50 * time.Millisecond)
}

// createLink calls the guarded public link flow. The server job slot is held
// by the caller; checkout.lock is taken here across the entire creation.
func (s *server) createLink(ctx context.Context, user, owner string, months int, report func(int, string)) (*checkout.Record, error) {
	lockFile, err := lockCheckout(ctx, s.dataDir)
	if err != nil {
		return nil, err
	}
	defer lockFile.Close()
	for i := 0; ; i++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		rec, err := checkout.PublicLinkForUsername(ctx, s.vault, user, owner, s.proxyPort, months)
		if err == nil {
			return rec, nil
		}
		var wait *checkout.CheckoutWaitError
		if !errors.As(err, &wait) || ctx.Err() != nil {
			return rec, err
		}
		remain := wait.Wait + time.Second
		if deadline, ok := ctx.Deadline(); ok {
			if limit := time.Until(deadline) - 5*time.Second; remain > limit {
				if limit <= 0 {
					return rec, err
				}
				remain = limit
			}
		}
		report(40, fmt.Sprintf("上一条链接的付款确认窗口尚未结束，约 %d 秒后自动继续…", int(math.Ceil(remain.Seconds()))))
		timer := time.NewTimer(remain)
		select {
		case <-ctx.Done():
			timer.Stop()
			return rec, ctx.Err()
		case <-timer.C:
		}
		report(30, "付款确认窗口已结束，继续生成链接…")
	}
}

// lockCheckout takes the process-wide checkout lock. A concurrent holder (for
// example an operator CLI) only delays the request; ctx cancellation wins.
func lockCheckout(ctx context.Context, dir string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(dir, "checkout.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func describeLinkError(err error) (code, message string) {
	switch {
	case errors.Is(err, checkout.ErrUserNotFound):
		return "user_not_found", "未找到该 X 账号，请确认用户名拼写"
	case errors.Is(err, checkout.ErrNotEligible):
		return "not_eligible", "该账号目前无法接收 Premium 赠品，未生成链接"
	case errors.Is(err, checkout.ErrXReadFailure):
		return "x_read_failed", "X 账号或价格查询失败，未生成链接；请稍后重试"
	case errors.Is(err, checkout.ErrVerifyUnpaid):
		return "needs_review", "原链接已失效且需人工核对是否已付款；本次未生成新链接"
	case errors.Is(err, checkout.ErrPublicPaymentDeclined):
		return "declined", "上一次付款被银行拒绝，可重新生成链接"
	case errors.Is(err, checkout.ErrPublicLinkConflict):
		return "conflict", "该账号存在未完成的订单记录，暂不能生成"
	case errors.Is(err, checkout.ErrPublicLinkPending):
		return "pending", "链接创建未完成，请稍后重试"
	case errors.Is(err, checkout.ErrCheckoutRateLimited):
		return "rate_limited", "生成排队超时，请稍后重试"
	case errors.Is(err, syscall.EWOULDBLOCK):
		return "busy", "另一个生成任务正在进行，请稍后重试"
	default:
		return "failed", "生成失败，请稍后重试"
	}
}

func resultEvent(r *checkout.Record) map[string]any {
	return map[string]any{
		"t":            "result",
		"username":     r.Username,
		"recipient_id": r.RecipientID,
		"months":       r.Months,
		"amount":       r.Amount,
		"currency":     r.Currency,
		"url":          r.URL,
		"session_id":   r.SessionID,
		"status":       r.Status,
		"created":      r.Created,
	}
}

func errorEvent(code, message, detail string) map[string]any {
	e := map[string]any{"t": "error", "code": code, "message": message}
	if detail != "" {
		e["detail"] = detail
	}
	return e
}

func progressEvent(percent int, message string) map[string]any {
	return map[string]any{"t": "progress", "percent": percent, "message": message}
}
