package site

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
	"xgift/internal/checkout"
)

type publicLinkJob struct {
	ID          string            `json:"id"`
	Owner       string            `json:"owner"`
	State       string            `json:"state"`
	Request     manualLinkRequest `json:"request"`
	Seen        time.Time         `json:"seen"`
	Left        time.Time         `json:"left"`
	Started     time.Time         `json:"started"`
	Finished    time.Time         `json:"finished"`
	NextAttempt time.Time         `json:"next_attempt"`
	Cancelled   bool              `json:"cancelled,omitempty"`
	Code        int               `json:"code"`
	Result      json.RawMessage   `json:"result,omitempty"`
}

// Queue tickets and their browser ownership survive restarts in the vault.
type publicLinkQueue struct {
	mu            sync.Mutex
	jobs          []*publicLinkJob
	preserveUntil time.Time
	blockedUntil  time.Time
	windowUser    string
}

// linkBuildTime is the typical time to verify X and create one checkout.
const linkBuildTime = 20 * time.Second

// prune drops abandoned and old tickets and saves when anything was removed.
// Caller holds the queue mutex.
func (s *server) prune(now time.Time) {
	q := &s.linkQueue
	if now.Before(q.preserveUntil) {
		return
	}
	keep := q.jobs[:0]
	for _, j := range q.jobs {
		if j.State == "queued" && (j.Cancelled || now.Sub(j.Seen) > 5*time.Minute || (!j.Left.IsZero() && now.Sub(j.Left) > 90*time.Second)) {
			continue
		}
		if j.State == "done" && now.Sub(j.Finished) > 15*time.Minute {
			continue
		}
		keep = append(keep, j)
	}
	changed := len(keep) != len(q.jobs)
	clear(q.jobs[len(keep):])
	q.jobs = keep
	if changed {
		if err := s.saveQueue(); err != nil {
			log.Printf("public queue checkpoint failed")
		}
	}
}

func (s *server) enqueuePublicLink(w http.ResponseWriter, request manualLinkRequest, owner string) {
	q := &s.linkQueue
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now()
	s.refreshPublicLinkWait(now)
	s.prune(now)
	for _, j := range q.jobs {
		if j.Owner == owner && j.State != "done" && !j.Cancelled {
			if j.Request != request {
				if j.State != "queued" || j.Request.Username != request.Username {
					continue
				}
				j.Request = request
				j.NextAttempt = time.Time{}
			}
			j.Seen, j.Left = now, time.Time{}
			if !s.savePublicQueueOrReply(w) {
				return
			}
			s.respondPublicLink(w, j)
			return
		}
	}
	if len(q.jobs) >= 500 {
		message(w, 503, "当前排队人数较多，请稍后重试。")
		return
	}
	j := &publicLinkJob{ID: token(24), Owner: owner, Request: request, State: "queued", Seen: now}
	q.jobs = append(q.jobs, j)
	if !s.savePublicQueueOrReply(w) {
		return
	}
	s.respondPublicLink(w, j)
}

// All callers hold the queue mutex; only this browser may read its ticket.
func (s *server) respondPublicLink(w http.ResponseWriter, job *publicLinkJob) {
	q := &s.linkQueue
	if job.Cancelled {
		message(w, 404, "已退出排队。")
		return
	}
	if job.State == "done" {
		// A delivered link is replayed only inside its payment window; a newer
		// link for the same account already replaced it (invalidateOlderPublicResults).
		if job.Code == 200 && !job.liveLink(time.Now()) {
			message(w, http.StatusGone, "上次的付款链接已结束，如需付款请重新排队。可用「查询付款状态」确认是否已付款。")
			return
		}
		if job.Code == 200 && job.liveLink(time.Now()) {
			// A saved response is history, not proof that a link remains payable.
			release, ok := s.tryLock()
			if !ok {
				message(w, 409, "正在核实原付款链接，请稍后查询。")
				return
			}
			defer release()
			ctx, cancel := context.WithTimeout(s.ctx, 8*time.Second)
			defer cancel()
			order, err := checkout.PublicOrderStatus(s.vault, job.Request.Username, job.Owner)
			var previous struct {
				URL string `json:"checkout_url"`
			}
			json.Unmarshal(job.Result, &previous)
			if err != nil || order == nil || order.URL != previous.URL {
				message(w, http.StatusGone, "历史付款链接已停止提供，请查询原订单付款状态。")
				return
			}
			if order.Status == "succeeded" {
				reply(w, 200, map[string]any{"status": "succeeded", "message": "原订单已付款成功，无需再次付款。"})
				return
			}
			if err = checkout.VerifyExistingPublicLink(ctx, s.vault, order); err != nil {
				message(w, 409, "原付款结果尚未核实，请查询付款状态；请勿重复付款。")
				return
			}
			if order.Status == "succeeded" {
				reply(w, 200, map[string]any{"status": "succeeded", "message": "原订单已付款成功，无需再次付款。"})
				return
			}
			if checkout.CheckoutLink(order) == "" || !checkout.PublicOrderOpen(order, time.Now()) {
				message(w, http.StatusGone, "原付款链接已停止提供，请查询付款状态。")
				return
			}
		}
		reply(w, job.Code, job.Result)
		return
	}
	position, estimate := q.estimate(job, time.Now())
	seconds := roundWait(estimate)
	waitText := fmt.Sprintf("%d 秒", seconds)
	if seconds >= 60 {
		waitText = fmt.Sprintf("%d 分钟", (seconds+59)/60)
	}
	msg := fmt.Sprintf("前方还有 %d 人，预计约 %s后生成链接。请保持页面打开。", position-1, waitText)
	if job.State == "processing" {
		msg = fmt.Sprintf("正在生成付款链接，预计还需约 %s。", waitText)
	}
	reply(w, http.StatusAccepted, map[string]any{"ticket": job.ID, "status": job.State, "position": position, "ahead": position - 1, "estimated_wait_seconds": seconds, "message": msg})
}

// estimate counts active tickets up to job; a nil job estimates a newcomer.
// Everyone ahead may hold a full payment window. Caller holds the queue mutex.
func (q *publicLinkQueue) estimate(job *publicLinkJob, now time.Time) (int, time.Duration) {
	if job != nil && q.windowUser != "" && job.Request.Username == q.windowUser {
		return 1, linkBuildTime // the window holder replaces its own link
	}
	position := 1
	for _, j := range q.jobs {
		if j == job {
			break
		}
		if j.State != "done" && !j.Cancelled {
			position++
		}
	}
	wait := max(q.blockedUntil.Sub(now), 0)
	return position, wait + time.Duration(position-1)*(checkout.PublicLinkTTL+linkBuildTime) + linkBuildTime
}

func roundWait(d time.Duration) int { return int((d+5*time.Second-1)/(5*time.Second)) * 5 }

// publicLinkQueueSummary is a read-only view for the form before joining.
func (s *server) publicLinkQueueSummary(w http.ResponseWriter, r *http.Request) {
	q := &s.linkQueue
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now()
	s.refreshPublicLinkWait(now)
	s.prune(now)
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
	defer q.mu.Unlock()
	now := time.Now()
	s.refreshPublicLinkWait(now)
	s.prune(now)
	for _, j := range q.jobs {
		if j.ID == r.PathValue("ticket") && j.Owner == c.Value && !j.Cancelled {
			j.Seen, j.Left = now, time.Time{}
			s.respondPublicLink(w, j)
			return
		}
	}
	message(w, 404, "排队记录已失效（离开本页或在后台停留过久会被移出队列）。请重新提交；系统会先核对原订单，不会重复扣款。")
}

// liveLink reports a delivered checkout link still inside its payment window.
func (j *publicLinkJob) liveLink(now time.Time) bool {
	if j.State != "done" || j.Code != 200 {
		return false
	}
	var result struct {
		URL       string `json:"checkout_url"`
		ExpiresAt int64  `json:"expires_at"`
	}
	return json.Unmarshal(j.Result, &result) == nil && result.URL != "" && result.ExpiresAt > now.Unix()
}

func (s *server) invalidateOlderPublicResults(username, currentURL string) {
	q := &s.linkQueue
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, j := range q.jobs {
		if j.State != "done" || j.Code != 200 || j.Request.Username != username {
			continue
		}
		var old struct {
			URL string `json:"checkout_url"`
		}
		if json.Unmarshal(j.Result, &old) == nil && old.URL != "" && old.URL != currentURL {
			j.Code = http.StatusConflict
			j.Result = json.RawMessage(`{"message":"付款链接已被新的请求替换，请使用最新链接。"}`)
		}
	}
	if err := s.saveQueue(); err != nil {
		log.Printf("public queue checkpoint failed")
	}
}

func (s *server) publicLinkQueueLoop() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-tick.C:
		}
		s.processPublicLinkQueue(s.ctx, s.createLink)
	}
}

func (s *server) processPublicLinkQueue(ctx context.Context, create func(context.Context, manualLinkRequest, string) linkOutcome) {
	q := &s.linkQueue
	q.mu.Lock()
	now := time.Now()
	s.prune(now)
	dispatchable := func(j *publicLinkJob) bool {
		return j.State == "queued" && j.Left.IsZero() && now.Sub(j.Seen) <= 90*time.Second
	}
	var job *publicLinkJob
	for _, j := range q.jobs {
		if j.State == "processing" {
			q.mu.Unlock()
			return
		}
		if job == nil && dispatchable(j) {
			job = j
		}
	}
	// Replacing the active user's own plan does not take another person's slot.
	if user, _, _, err := checkout.PublicCheckoutWindow(s.vault, now); err == nil && user != "" {
		for _, j := range q.jobs {
			if dispatchable(j) && j.Request.Username == user {
				job = j
				break
			}
		}
	}
	if job == nil || now.Before(job.NextAttempt) {
		q.mu.Unlock()
		return
	}
	job.State, job.Started = "processing", now
	if err := s.saveQueue(); err != nil {
		job.State = "queued"
		q.mu.Unlock()
		log.Printf("public queue checkpoint failed; creation deferred")
		return
	}
	q.mu.Unlock()
	out := create(ctx, job.Request, job.Owner)
	q.mu.Lock()
	defer q.mu.Unlock()
	defer func() {
		if err := s.saveQueue(); err != nil {
			log.Printf("public queue completion checkpoint failed")
		}
	}()
	switch {
	case job.Cancelled:
		q.remove(job)
	case ctx.Err() != nil:
		// Shutdown keeps the ticket; the next start reconciles the ledger.
		job.State, job.NextAttempt = "queued", time.Time{}
	case out.declined:
		// A slot holder who retries after refusal loses the own-window shortcut.
		// Keep their ticket, but put this explicit retry behind everyone.
		job.State, job.NextAttempt = "queued", time.Time{}
		q.blockedUntil, q.windowUser = time.Time{}, ""
		q.remove(job)
		q.jobs = append(q.jobs, job)
	case out.retryIn > 0:
		job.State = "queued"
		if out.blocked > 0 {
			q.blockedUntil = time.Now().Add(out.blocked)
		}
		job.NextAttempt = time.Now().Add(out.retryIn)
	default:
		q.blockedUntil = time.Time{}
		job.State, job.Finished, job.Code = "done", time.Now(), out.status
		// A stored result never changes, so it must not look like a transient
		// gateway error that browsers keep retrying.
		if job.Code >= 500 {
			job.Code = http.StatusUnprocessableEntity
		}
		job.Result, _ = json.Marshal(out.body)
	}
}

func (q *publicLinkQueue) remove(job *publicLinkJob) {
	for i, j := range q.jobs {
		if j == job {
			q.jobs = append(q.jobs[:i], q.jobs[i+1:]...)
			return
		}
	}
}

// Caller holds the queue mutex. Reads persisted state so a restart or a new
// browser sees the current payment window before its first worker attempt.
func (s *server) refreshPublicLinkWait(now time.Time) {
	if user, _, _, err := checkout.PublicCheckoutWindow(s.vault, now); err == nil {
		s.linkQueue.windowUser = user
	}
	if wait, err := checkout.CheckoutCreationWait(s.vault, now); err == nil {
		s.linkQueue.blockedUntil = now.Add(wait)
	}
}
