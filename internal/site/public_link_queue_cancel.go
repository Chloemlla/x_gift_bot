package site

import (
	"log"
	"net/http"
	"time"
)

// The middleware enforces same-origin POSTs. Both the opaque ticket and browser
// cookie must match; no username-only cancellation or deletion of orders.
func (s *server) cancelPublicLinkQueue(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("__Host-xgift-link")
	if err != nil {
		message(w, 404, "排队记录不存在。")
		return
	}
	q := &s.linkQueue
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, j := range q.jobs {
		if j.ID != r.PathValue("ticket") || j.Owner != cookie.Value {
			continue
		}
		if j.State == "done" {
			reply(w, 200, map[string]any{"cancelled": false})
			return
		}
		// Keep an in-flight worker tracked until it returns, but never retry it.
		previous := j.Cancelled
		j.Cancelled = true
		if !s.savePublicQueueOrReply(w) {
			j.Cancelled = previous
			return
		}
		if j.State == "queued" {
			q.jobs = append(q.jobs[:i], q.jobs[i+1:]...)
			if err := s.saveQueue(); err != nil {
				log.Printf("public queue checkpoint failed") // the durable tombstone still prevents restoration
			}
		}
		reply(w, 200, map[string]any{"cancelled": true})
		return
	}
	// Idempotent cancellation, without disclosing another browser's ownership.
	reply(w, 200, map[string]any{"cancelled": true})
}

// A pagehide event also fires on refresh. Pause dispatch immediately, then let
// the same browser reclaim its ticket before retiring it after a short grace.
func (s *server) leavePublicLinkQueue(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie("__Host-xgift-link")
	if err != nil {
		message(w, 404, "排队记录不存在。")
		return
	}
	q := &s.linkQueue
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, j := range q.jobs {
		if j.ID == r.PathValue("ticket") && j.Owner == c.Value && !j.Cancelled && j.State != "done" {
			j.Left = time.Now()
			break
		}
	}
	reply(w, 200, map[string]bool{"leaving": true})
}

// Recover only tickets belonging to this browser, never search by username.
// Finished tickets are recovered only while their payment link is still live;
// failures, payments and expired links are not replayed on a later visit.
func (s *server) currentPublicLinkQueue(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie("__Host-xgift-link")
	if err != nil {
		message(w, 404, "没有待恢复的排队。")
		return
	}
	q := &s.linkQueue
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now()
	s.prune(now)
	var found *publicLinkJob
	for _, j := range q.jobs {
		if j.Owner == c.Value && !j.Cancelled && (j.State != "done" || (found == nil && j.liveLink(now))) {
			found = j
		}
	}
	if found == nil {
		message(w, 404, "没有待恢复的排队。")
		return
	}
	found.Seen, found.Left = now, time.Time{}
	reply(w, 200, map[string]any{"ticket": found.ID, "username": found.Request.Username, "months": found.Request.Months})
}
