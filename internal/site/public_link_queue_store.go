package site

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"
	"xgift/internal/checkout"
)

// v2 stores times as RFC 3339; an unreadable older snapshot starts empty.
const publicQueueKey = "public-link-queue:v2"

// saveQueue encrypts ownership cookies and checkout URLs with the vault.
// Admission and processing transitions reach disk before acknowledgement.
// Caller holds the queue mutex.
func (s *server) saveQueue() error {
	b, err := json.Marshal(s.linkQueue.jobs)
	if err != nil {
		return err
	}
	defer clear(b)
	return s.vault.Put(publicQueueKey, b)
}

func (s *server) savePublicQueueOrReply(w http.ResponseWriter) bool {
	if err := s.saveQueue(); err != nil {
		log.Printf("public queue admission checkpoint failed")
		message(w, http.StatusServiceUnavailable, "暂时无法保存排队信息，请稍后重试。")
		return false
	}
	return true
}

// Restore before starting HTTP or workers. In-flight requests re-enter the same
// position and use the checkout ledger to reconcile any completed upstream order.
// The queue is soft state: a damaged snapshot or ticket is dropped, never fatal.
func (s *server) restorePublicLinkQueue() error {
	b, err := s.vault.Get(publicQueueKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read public queue: %w", err)
	}
	defer clear(b)
	var saved []*publicLinkJob
	if err = json.Unmarshal(b, &saved); err != nil {
		log.Printf("public queue snapshot unreadable; starting empty")
	}
	now := time.Now()
	cat, catalogErr := checkout.ReadCatalog(s.vault)
	q := &s.linkQueue
	q.jobs = nil
	for _, j := range saved {
		if j == nil || j.Cancelled || j.ID == "" || j.Owner == "" || !validUsername(j.Request.Username) {
			continue
		}
		if j.State != "done" {
			if _, err := cat.PlanFor(j.Request.Months); catalogErr == nil && err != nil {
				continue
			}
			// Give existing browsers a full reconnect grace period after downtime.
			j.State, j.Seen, j.Started, j.NextAttempt = "queued", now, time.Time{}, time.Time{}
		}
		q.jobs = append(q.jobs, j)
	}
	s.prune(now)
	if err := s.saveQueue(); err != nil {
		return err
	}
	s.refreshPublicLinkWait(now)
	log.Printf("public queue restored: tickets=%d", len(q.jobs))
	return nil
}

func validUsername(s string) bool {
	user, ok := checkout.NormalizeUsername(s)
	return ok && user == s
}
