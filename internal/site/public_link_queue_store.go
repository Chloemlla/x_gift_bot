package site

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"
)

const publicQueueKey = "public-link-queue:v1"

type savedPublicQueue struct {
	Version         int              `json:"version"`
	SavedAt         int64            `json:"saved_at"`
	AverageDuration time.Duration    `json:"average_duration_ns"`
	Jobs            []savedPublicJob `json:"jobs"`
}

type savedPublicJob struct {
	ID          string            `json:"id"`
	Owner       string            `json:"owner"`
	State       string            `json:"state"`
	Request     manualLinkRequest `json:"request"`
	Left        int64             `json:"left,omitempty"`
	Seen        int64             `json:"seen"`
	Finished    int64             `json:"finished"`
	NextAttempt int64             `json:"next_attempt"`
	Started     int64             `json:"started"`
	Cancelled   bool              `json:"cancelled,omitempty"`
	Code        int               `json:"code"`
	Result      []byte            `json:"result,omitempty"`
}

func queueMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}
func queueTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// Caller holds the queue mutex. Encrypt ownership cookies and checkout URLs with
// the vault; admission and processing transitions reach disk before acknowledgement.
func (s *server) persistPublicLinkQueueLocked() error {
	q := &s.linkQueue
	if !q.dirty || s.vault == nil {
		return nil
	}
	saved := savedPublicQueue{Version: 1, SavedAt: time.Now().UnixMilli(), AverageDuration: q.averageDuration, Jobs: make([]savedPublicJob, 0, len(q.jobs))}
	for _, j := range q.jobs {
		saved.Jobs = append(saved.Jobs, savedPublicJob{ID: j.id, Owner: j.owner, State: j.state, Request: j.request, Seen: queueMillis(j.seen), Finished: queueMillis(j.finished), NextAttempt: queueMillis(j.nextAttempt), Started: queueMillis(j.started), Code: j.code, Result: j.result, Cancelled: j.cancelled, Left: queueMillis(j.left)})
	}
	b, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	defer clear(b)
	if err = s.vault.Put(publicQueueKey, b); err != nil {
		return err
	}
	q.dirty = false
	q.lastPersist = time.Now()
	return nil
}

func (s *server) savePublicQueueOrReply(w http.ResponseWriter) bool {
	if err := s.persistPublicLinkQueueLocked(); err != nil {
		log.Printf("public queue admission checkpoint failed")
		message(w, http.StatusServiceUnavailable, "暂时无法保存排队信息，请稍后重试。")
		return false
	}
	return true
}

// Restore before starting HTTP or workers. In-flight requests re-enter the same
// position and use the checkout ledger to reconcile any completed upstream order.
func (s *server) restorePublicLinkQueue() error {
	b, err := s.vault.Get(publicQueueKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read public queue: %w", err)
	}
	defer clear(b)
	var saved savedPublicQueue
	if err = json.Unmarshal(b, &saved); err != nil {
		return errors.New("invalid public queue snapshot")
	}
	if saved.Version != 1 || len(saved.Jobs) > 500 || saved.AverageDuration < 0 {
		return errors.New("invalid public queue snapshot")
	}
	q := &s.linkQueue
	now := time.Now()
	jobs := make([]*publicLinkJob, 0, len(saved.Jobs))
	ids := make(map[string]bool)
	for _, j := range saved.Jobs {
		if j.ID == "" || ids[j.ID] || j.Owner == "" || j.Request.Username == "" || (j.Request.Months != 3 && j.Request.Months != 6) || (j.State != "queued" && j.State != "processing" && j.State != "done") || (j.State == "done" && (j.Code < 200 || j.Code > 599 || !json.Valid(j.Result))) {
			return errors.New("invalid public queue ticket")
		}
		ids[j.ID] = true
		if j.Cancelled {
			continue
		}
		job := &publicLinkJob{id: j.ID, owner: j.Owner, state: j.State, left: queueTime(j.Left), request: j.Request, seen: queueTime(j.Seen), finished: queueTime(j.Finished), nextAttempt: queueTime(j.NextAttempt), started: queueTime(j.Started), code: j.Code, result: j.Result}
		if job.state != "done" {
			// Give existing browsers a full reconnect grace period after downtime.
			job.state = "queued"
			job.seen = now
			job.started = time.Time{}
			job.nextAttempt = time.Time{}
		}
		jobs = append(jobs, job)
	}
	q.jobs, q.averageDuration = jobs, saved.AverageDuration
	q.prune(now)
	q.dirty = true
	if err := s.persistPublicLinkQueueLocked(); err != nil {
		return err
	}
	s.refreshPublicLinkWait(now)
	log.Printf("public queue restored: tickets=%d", len(q.jobs))
	return nil
}
