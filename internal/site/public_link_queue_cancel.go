package site

import (
	"net/http"
	"time"
)

// The middleware enforces same-origin POSTs. Both the opaque ticket and browser
// cookie must match; no username-only cancellation or deletion of orders.
func (s *server) cancelPublicLinkQueue(w http.ResponseWriter, r *http.Request) {
	// Old pages send an empty beacon on refresh as well as close. Treat it
	// as a disconnect lease; explicit button requests carry a JSON body.
	if r.ContentLength == 0 {
		s.leavePublicLinkQueue(w, r)
		return
	}
	cookie, err := r.Cookie("__Host-xgift-link")
	if err != nil {
		message(w, 404, "排队记录不存在。")
		return
	}
	q := &s.linkQueue
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, j := range q.jobs {
		if j.id != r.PathValue("ticket") || j.owner != cookie.Value {
			continue
		}
		if j.state == "done" {
			reply(w, 200, map[string]any{"cancelled": false})
			return
		}
		// Keep an in-flight worker tracked until it returns, but never retry it.
		previous := j.cancelled
		j.cancelled = true
		q.dirty = true
		if !s.savePublicQueueOrReply(w) {
			j.cancelled = previous
			return
		}
		if j.state == "queued" {
			q.jobs = append(q.jobs[:i], q.jobs[i+1:]...)
			q.dirty = true // durable tombstone already prevents restoration
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
		if j.id == r.PathValue("ticket") && j.owner == c.Value && !j.cancelled && j.state != "done" {
			j.left = time.Now()
			q.dirty = true
			if !s.savePublicQueueOrReply(w) {
				return
			}
			break
		}
	}
	reply(w, 200, map[string]bool{"leaving": true})
}

// Recover only tickets belonging to this browser, never search by username.
func (s *server) currentPublicLinkQueue(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie("__Host-xgift-link")
	if err != nil {
		message(w, 404, "没有待恢复的排队。")
		return
	}
	q := &s.linkQueue
	q.mu.Lock()
	defer q.mu.Unlock()
	q.prune(time.Now())
	var found *publicLinkJob
	for _, j := range q.jobs {
		if j.owner == c.Value && !j.cancelled && (found == nil || j.state != "done") {
			found = j
		}
	}
	if found == nil {
		message(w, 404, "没有待恢复的排队。")
		return
	}
	found.seen = time.Now()
	found.left = time.Time{}
	q.dirty = true
	if !s.savePublicQueueOrReply(w) {
		return
	}
	reply(w, 200, map[string]any{"ticket": found.id, "username": found.request.Username, "months": found.request.Months})
}
