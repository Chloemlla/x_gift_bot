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

// v2 stores times as RFC 3339; unreadable snapshots fail closed.
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
// A damaged snapshot or ticket stops startup rather than losing waiting users.
func (s *server) restorePublicLinkQueue() error {
	b, err := s.vault.Get(publicQueueKey)
	var saved []*publicLinkJob
	if errors.Is(err, sql.ErrNoRows) {
		if saved, err = s.readV1Queue(); err != nil {
			return err
		}
	} else if err != nil {
		return fmt.Errorf("read public queue: %w", err)
	} else {
		defer clear(b)
		if err = json.Unmarshal(b, &saved); err != nil {
			return fmt.Errorf("public queue snapshot unreadable; refusing to discard tickets")
		}
	}
	if saved == nil {
		return nil
	}
	now := time.Now()
	cat, catalogErr := checkout.ReadCatalog(s.vault)
	q := &s.linkQueue
	q.jobs = nil
	for _, j := range saved {
		if j == nil || j.ID == "" || j.Owner == "" || !validUsername(j.Request.Username) {
			return errors.New("invalid saved public queue ticket; refusing to discard it")
		}
		if j.State != "done" {
			if _, err := cat.PlanFor(j.Request.Months); catalogErr != nil || err != nil {
				return errors.New("saved public queue plan unavailable; refusing to discard tickets")
			}
			// Give existing browsers a full reconnect grace period after downtime.
			j.State, j.Seen, j.Left, j.Started, j.NextAttempt = "queued", now, time.Time{}, time.Time{}, time.Time{}
		}
		q.jobs = append(q.jobs, j)
	}
	q.preserveUntil = now.Add(5 * time.Minute)
	// Preserve every saved ticket and its position across rollout, including
	// completed history. Normal request-time pruning remains separate.
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

// readV1Queue carries the queue across the v1→v2 deploy so waiting users keep
// their places. Delete once production has started with v2.
func (s *server) readV1Queue() ([]*publicLinkJob, error) {
	b, err := s.vault.Get("public-link-queue:v1")
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read v1 public queue: %w", err)
	}
	defer clear(b)
	var v1 struct {
		Jobs []struct {
			ID          string            `json:"id"`
			Owner       string            `json:"owner"`
			State       string            `json:"state"`
			Request     manualLinkRequest `json:"request"`
			Left        int64             `json:"left"`
			Seen        int64             `json:"seen"`
			Finished    int64             `json:"finished"`
			NextAttempt int64             `json:"next_attempt"`
			Started     int64             `json:"started"`
			Cancelled   bool              `json:"cancelled"`
			Code        int               `json:"code"`
			Result      []byte            `json:"result"`
		} `json:"jobs"`
	}
	if err = json.Unmarshal(b, &v1); err != nil {
		return nil, errors.New("v1 public queue unreadable; refusing to discard tickets")
	}
	ms := func(v int64) time.Time {
		if v == 0 {
			return time.Time{}
		}
		return time.UnixMilli(v)
	}
	jobs := make([]*publicLinkJob, 0, len(v1.Jobs))
	for _, j := range v1.Jobs {
		jobs = append(jobs, &publicLinkJob{ID: j.ID, Owner: j.Owner, State: j.State, Request: j.Request, Seen: ms(j.Seen), Left: ms(j.Left), Started: ms(j.Started), Finished: ms(j.Finished), NextAttempt: ms(j.NextAttempt), Cancelled: j.Cancelled, Code: j.Code, Result: j.Result})
	}
	return jobs, nil
}
