package site

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

type publicLinkJob struct {
	id, owner, state            string
	request                     manualLinkRequest
	seen, finished, nextAttempt time.Time
	started                     time.Time
	code                        int
	result                      []byte
}

// Tickets live only for this process. Restarted clients must explicitly requeue;
// the encrypted checkout ledger still prevents ambiguous payment retries.
type publicLinkQueue struct {
	mu              sync.Mutex
	jobs            []*publicLinkJob
	averageDuration time.Duration
}

func (q *publicLinkQueue) prune(now time.Time) {
	keep := q.jobs[:0]
	for _, j := range q.jobs {
		if j.state == "queued" && now.Sub(j.seen) > 90*time.Second {
			continue
		}
		if j.state == "done" && now.Sub(j.finished) > 10*time.Minute {
			continue
		}
		keep = append(keep, j)
	}
	clear(q.jobs[len(keep):])
	q.jobs = keep
}

func (s *server) enqueuePublicLink(w http.ResponseWriter, request manualLinkRequest, owner string) {
	q := &s.linkQueue
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now()
	q.prune(now)
	for _, j := range q.jobs {
		if j.owner == owner && j.state != "done" {
			if j.request != request {
				message(w, 409, "当前浏览器已有请求排队，请等待完成后再生成其他订单。")
				return
			}
			j.seen = now
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
	q.respond(w, j)
}

// All callers hold the queue mutex; only this browser may read its ticket.
func (q *publicLinkQueue) respond(w http.ResponseWriter, job *publicLinkJob) {
	if job.state == "done" {
		// A delayed poll must never deliver an old success after a later checkout
		// may have invalidated it. A new submission always performs fresh checks.
		if job.code == 200 && time.Since(job.finished) >= 15*time.Second {
			message(w, 409, "本次链接未及时领取，请重新生成新链接；若已付款，请勿重复支付。")
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(job.code)
		w.Write(job.result)
		return
	}
	position := 0
	estimate := time.Duration(0)
	now := time.Now()
	average := q.averageDuration
	if average < 20*time.Second {
		average = 20 * time.Second
	}
	for _, j := range q.jobs {
		if j.state != "done" {
			position++
			remaining := average
			if !j.started.IsZero() {
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
			break
		}
	}
	seconds := int((estimate+5*time.Second-1)/(5*time.Second)) * 5
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

func (s *server) publicLinkQueueStatus(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie("__Host-xgift-link")
	if err != nil {
		message(w, 404, "排队记录已失效，请重新提交。")
		return
	}
	q := &s.linkQueue
	q.mu.Lock()
	defer q.mu.Unlock()
	q.prune(time.Now())
	for _, j := range q.jobs {
		if j.id == r.PathValue("ticket") && j.owner == c.Value {
			j.seen = time.Now()
			q.respond(w, j)
			return
		}
	}
	message(w, 404, "排队记录已失效或服务已重启，请重新提交；系统会先核对原订单。")
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
	var job *publicLinkJob
	for _, j := range q.jobs {
		if j.state == "processing" {
			q.mu.Unlock()
			return
		}
		if j.state == "queued" {
			job = j
			break
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
	if job.started.IsZero() {
		job.started = time.Now()
	}
	q.mu.Unlock()
	w := &linkResponse{header: make(http.Header), code: 200}
	r, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost/api/manual-link", nil)
	execute(w, r, job.request, job.owner)
	q.mu.Lock()
	defer q.mu.Unlock()
	if w.Header().Get("Retry-After") != "" {
		job.state = "queued"
		seconds, _ := strconv.Atoi(w.Header().Get("Retry-After"))
		if seconds < 1 {
			seconds = 3
		}
		job.nextAttempt = time.Now().Add(time.Duration(seconds) * time.Second)
		return
	}
	job.state, job.finished, job.code = "done", time.Now(), w.code
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
