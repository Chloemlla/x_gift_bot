package site

import "net/http"

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
