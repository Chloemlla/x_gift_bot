package site

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"
	"xgift/internal/checkout"
)

type publicLinkJob struct {
	id, owner, state            string
	request                     manualLinkRequest
	seen, finished, nextAttempt time.Time
	started                     time.Time
	left                        time.Time
	code                        int
	cancelled                   bool
	result                      []byte
}

// Queue tickets and their browser ownership survive restarts in the vault.
type publicLinkQueue struct {
	mu              sync.Mutex
	jobs            []*publicLinkJob
	averageDuration time.Duration
	blockedUntil    time.Time
	windowUser      string
	dirty           bool
	lastPersist     time.Time
}

func (q *publicLinkQueue) prune(now time.Time) {
	keep := q.jobs[:0]
	for _, j := range q.jobs {
		if j.state == "queued" && (j.cancelled || now.Sub(j.seen) > 5*time.Minute || (!j.left.IsZero() && now.Sub(j.left) > 90*time.Second)) {
			continue
		}
		if j.state == "done" && now.Sub(j.finished) > 15*time.Minute {
			continue
		}
		keep = append(keep, j)
	}
	if len(keep) != len(q.jobs) {
		q.dirty = true
	}
	clear(q.jobs[len(keep):])
	q.jobs = keep
}

func (s *server) enqueuePublicLink(w http.ResponseWriter, request manualLinkRequest, owner string) {
	q := &s.linkQueue
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now()
	s.refreshPublicLinkWait(now)
	q.prune(now)
	for _, j := range q.jobs {
		if j.owner == owner && j.state != "done" && !j.cancelled {
			if j.request != request {
				if j.state != "queued" || j.request.Username != request.Username {
					continue
				}
				j.request = request
				j.nextAttempt = time.Time{}
			}
			j.seen = now
			j.left = time.Time{}
			q.dirty = true
			if !s.savePublicQueueOrReply(w) {
				return
			}
			q.respond(w, j)
			return
		}
	}
	if len(q.jobs) >= 500 {
		message(w, 503, "当前排队人数较多，请稍后重试。")
		return
	}
	j := &publicLinkJob{id: token(24), owner: owner, request: request, state: "queued", seen: now}
	q.jobs = append(q.jobs, j)
	q.dirty = true
	if !s.savePublicQueueOrReply(w) {
		return
	}
	q.respond(w, j)
}

// All callers hold the queue mutex; only this browser may read its ticket.
func (q *publicLinkQueue) respond(w http.ResponseWriter, job *publicLinkJob) {
	if job.cancelled {
		message(w, 404, "已退出排队。")
		return
	}
	if job.state == "done" {
		// A delayed poll must never deliver an old success after a later checkout
		// may have invalidated it. A new submission always performs fresh checks.
		if job.code == 200 && time.Since(job.finished) >= 15*time.Second {
			message(w, 409, "本次链接需要重新核验，请重新获取付款链接；若已付款，请勿重复支付。")
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(job.code)
		w.Write(job.result)
		return
	}
	position, estimate := q.estimate(job, time.Now())
	seconds := roundWait(estimate)
	waitText := fmt.Sprintf("%d 秒", seconds)
	if seconds >= 60 {
		waitText = fmt.Sprintf("%d 分钟", (seconds+59)/60)
	}
	msg := fmt.Sprintf("前方还有 %d 人，预计约 %s后生成链接。请保持页面打开。", position-1, waitText)
	if job.state == "processing" {
		msg = fmt.Sprintf("正在生成付款链接，预计还需约 %s。", waitText)
	}
	reply(w, http.StatusAccepted, map[string]any{"ticket": job.id, "status": job.state, "position": position, "ahead": position - 1, "estimated_wait_seconds": seconds, "message": msg})
}

// estimate walks the queue up to job; a nil job estimates a newcomer joining
// at the end. Caller holds the queue mutex.
func (q *publicLinkQueue) estimate(job *publicLinkJob, now time.Time) (int, time.Duration) {
	position := 0
	estimate := q.blockedUntil.Sub(now)
	if estimate < 0 {
		estimate = 0
	}
	average := q.averageDuration
	if average < 20*time.Second {
		average = 20 * time.Second
	}
	ownWindow := job != nil && q.windowUser != "" && job.request.Username == q.windowUser
	if ownWindow {
		estimate = 0
	}
	for _, j := range q.jobs {
		if ownWindow && j != job {
			continue
		}
		if j.state != "done" && !j.cancelled {
			if position > 0 {
				estimate += checkout.PublicLinkTTL
			}
			position++
			remaining := average
			if j.state == "processing" && !j.started.IsZero() {
				remaining -= now.Sub(j.started)
			}
			if wait := j.nextAttempt.Sub(now) + 5*time.Second; wait > remaining {
				remaining = wait
			}
			if remaining < 5*time.Second {
				remaining = 5 * time.Second
			}
			estimate += remaining
		}
		if j == job {
			return position, estimate
		}
	}
	if position > 0 {
		estimate += checkout.PublicLinkTTL
	}
	return position + 1, estimate + average
}

func roundWait(d time.Duration) int { return int((d+5*time.Second-1)/(5*time.Second)) * 5 }

// publicLinkQueueSummary is a read-only view for the form before joining.
func (s *server) publicLinkQueueSummary(w http.ResponseWriter, r *http.Request) {
	q := &s.linkQueue
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now()
	s.refreshPublicLinkWait(now)
	q.prune(now)
	position, estimate := q.estimate(nil, now)
	reply(w, 200, map[string]int{"waiting": position - 1, "estimated_wait_seconds": roundWait(estimate)})
}

func (s *server) publicLinkQueueStatus(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie("__Host-xgift-link")
	if err != nil {
		message(w, 404, "排队记录已失效，请重新提交。")
		return
	}
	q := &s.linkQueue
	q.mu.Lock()
	s.refreshPublicLinkWait(time.Now())
	q.prune(time.Now())
	for _, j := range q.jobs {
		if j.id == r.PathValue("ticket") && j.owner == c.Value && !j.cancelled {
			j.seen = time.Now()
			j.left = time.Time{}
			q.dirty = true
			if j.state == "done" && j.code == 200 && time.Since(j.finished) >= 15*time.Second {
				if !j.liveLink(time.Now()) {
					// Paid or expired: the page returns to a clean form.
					q.mu.Unlock()
					message(w, http.StatusGone, "上次的付款链接已结束，如需付款请重新排队。")
					return
				}
				request := j.request
				q.mu.Unlock()
				// A delayed poll revalidates its link; it must never create or switch plans.
				if s.tryServePublicLink(w, r, request, c.Value, false) {
					return
				}
				message(w, 409, "这条付款链接已失效（可能已超时、付款被拒或已被新的请求替换）。如尚未付款，请重新排队获取新链接；如已付款，请勿重复支付。")
				return
			}
			q.respond(w, j)
			q.mu.Unlock()
			return
		}
	}
	q.mu.Unlock()
	message(w, 404, "排队记录已失效（离开本页或在后台停留过久会被移出队列）。请重新提交；系统会先核对原订单，不会重复扣款。")
}

// liveLink reports a delivered checkout link still inside its payment window.
func (j *publicLinkJob) liveLink(now time.Time) bool {
	if j.state != "done" || j.code != 200 {
		return false
	}
	var result struct {
		URL       string `json:"checkout_url"`
		ExpiresAt int64  `json:"expires_at"`
	}
	return json.Unmarshal(j.result, &result) == nil && result.URL != "" && result.ExpiresAt > now.Unix()
}

func (s *server) invalidateOlderPublicResults(username, currentURL string) {
	q := &s.linkQueue
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, j := range q.jobs {
		if j.state != "done" || j.code != 200 || j.request.Username != username {
			continue
		}
		var old struct {
			URL string `json:"checkout_url"`
		}
		if json.Unmarshal(j.result, &old) == nil && old.URL != "" && old.URL != currentURL {
			j.code = http.StatusConflict
			j.result = []byte(`{"message":"付款链接已被新的请求替换，请使用最新链接。"}`)
			q.dirty = true
		}
	}
	if err := s.persistPublicLinkQueueLocked(); err != nil {
		log.Printf("public queue checkpoint failed")
	}
}

type linkResponse struct {
	header http.Header
	code   int
	bytes.Buffer
}

func (w *linkResponse) Header() http.Header  { return w.header }
func (w *linkResponse) WriteHeader(code int) { w.code = code }

func (s *server) publicLinkQueueLoop() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-tick.C:
		}
		s.processPublicLinkQueue(s.ctx, s.executeManualLink)
	}
}

func (s *server) processPublicLinkQueue(ctx context.Context, execute func(http.ResponseWriter, *http.Request, manualLinkRequest, string)) {
	q := &s.linkQueue
	q.mu.Lock()
	q.prune(time.Now())
	if q.dirty && time.Since(q.lastPersist) >= 10*time.Second {
		if err := s.persistPublicLinkQueueLocked(); err != nil {
			q.mu.Unlock()
			log.Printf("public queue checkpoint failed")
			return
		}
	}
	var job *publicLinkJob
	for _, j := range q.jobs {
		if j.state == "processing" {
			q.mu.Unlock()
			return
		}
		if j.state == "queued" && j.left.IsZero() && time.Since(j.seen) <= 90*time.Second && job == nil {
			job = j
		}
	}
	// Replacing the active user's own plan does not take another person's slot.
	if s.vault != nil {
		if user, _, _, err := checkout.PublicCheckoutWindow(s.vault, time.Now()); err == nil && user != "" {
			for _, candidate := range q.jobs {
				if candidate.state == "queued" && candidate.left.IsZero() && time.Since(candidate.seen) <= 90*time.Second && candidate.request.Username == user {
					job = candidate
					break
				}
			}
		}
	}
	if job == nil {
		q.mu.Unlock()
		return
	}
	if time.Now().Before(job.nextAttempt) {
		q.mu.Unlock()
		return
	}
	job.state = "processing"
	job.started = time.Now()
	q.dirty = true
	if err := s.persistPublicLinkQueueLocked(); err != nil {
		job.state = "queued"
		q.mu.Unlock()
		log.Printf("public queue checkpoint failed; creation deferred")
		return
	}
	q.mu.Unlock()
	w := &linkResponse{header: make(http.Header), code: 200}
	r, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost/api/manual-link", nil)
	execute(w, r, job.request, job.owner)
	q.mu.Lock()
	defer q.mu.Unlock()
	defer func() {
		q.dirty = true
		if err := s.persistPublicLinkQueueLocked(); err != nil {
			log.Printf("public queue completion checkpoint failed")
		}
	}()
	// A shutdown-cancelled operation keeps its ticket and reconciles the ledger
	// on the next start instead of publishing a transient cancellation error.
	if job.cancelled {
		for i, candidate := range q.jobs {
			if candidate == job {
				q.jobs = append(q.jobs[:i], q.jobs[i+1:]...)
				break
			}
		}
		return
	}
	if ctx.Err() != nil {
		job.state = "queued"
		job.nextAttempt = time.Time{}
		return
	}
	if w.Header().Get("X-Checkout-Requeue") == "declined" {
		// A slot holder who retries after refusal loses the own-window shortcut.
		// Keep their ticket durable, but put this explicit retry behind everyone.
		job.state = "queued"
		job.nextAttempt = time.Time{}
		q.blockedUntil = time.Time{}
		q.windowUser = ""
		for i, candidate := range q.jobs {
			if candidate == job {
				q.jobs = append(append(q.jobs[:i], q.jobs[i+1:]...), job)
				break
			}
		}
		return
	}
	if w.Header().Get("Retry-After") != "" {
		job.state = "queued"
		if wait, _ := strconv.Atoi(w.Header().Get("X-Checkout-Wait-Seconds")); wait > 0 {
			q.blockedUntil = time.Now().Add(time.Duration(wait) * time.Second)
		}
		seconds, _ := strconv.Atoi(w.Header().Get("Retry-After"))
		if seconds < 1 {
			seconds = 3
		}
		job.nextAttempt = time.Now().Add(time.Duration(seconds) * time.Second)
		return
	}
	q.blockedUntil = time.Time{}
	job.state, job.finished, job.code = "done", time.Now(), w.code
	// A stored result never changes, so it must not look like a transient
	// gateway error that browsers keep retrying.
	if job.code >= 500 {
		job.code = http.StatusUnprocessableEntity
	}
	if job.code == 200 {
		duration := job.finished.Sub(job.started)
		if duration < 20*time.Second {
			duration = 20 * time.Second
		}
		if q.averageDuration == 0 {
			q.averageDuration = duration
		} else {
			q.averageDuration = (q.averageDuration*3 + duration) / 4
		}
	}
	job.result = append([]byte(nil), w.Bytes()...)
}

// Caller holds the queue mutex. Reads persisted state so a restart or a new
// browser sees the current payment window before its first worker attempt.
func (s *server) refreshPublicLinkWait(now time.Time) {
	if s.vault == nil {
		return
	}
	if user, _, _, err := checkout.PublicCheckoutWindow(s.vault, now); err == nil {
		s.linkQueue.windowUser = user
	}
	if wait, err := checkout.CheckoutCreationWait(s.vault, now); err == nil {
		s.linkQueue.blockedUntil = now.Add(wait)
	}
}
