package site

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"xgift/internal/vault"
)

func TestPublicQueueFIFOOwnershipDeduplicationAndRateWait(t *testing.T) {
	s := &server{}
	first := manualLinkRequest{Username: "first", Months: 3}
	enqueue := func(r manualLinkRequest, owner string) string {
		w := httptest.NewRecorder()
		s.enqueuePublicLink(w, r, owner)
		if w.Code != 202 {
			t.Fatal(w.Code, w.Body.String())
		}
		var result struct{ Ticket string }
		json.Unmarshal(w.Body.Bytes(), &result)
		return result.Ticket
	}
	a := enqueue(first, "owner-a")
	if a != enqueue(first, "owner-a") || len(s.linkQueue.jobs) != 1 {
		t.Fatal("duplicate queued twice")
	}
	enqueue(manualLinkRequest{Username: "second", Months: 6}, "owner-b")
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/manual-link/queue/"+a, nil)
	r.SetPathValue("ticket", a)
	r.AddCookie(&http.Cookie{Name: "__Host-xgift-link", Value: "owner-b"})
	s.publicLinkQueueStatus(w, r)
	if w.Code != 404 {
		t.Fatal("another browser read queue ticket")
	}
	var calls []string
	limited := true
	execute := func(w http.ResponseWriter, r *http.Request, q manualLinkRequest, owner string) {
		calls = append(calls, q.Username)
		if limited {
			w.Header().Set("Retry-After", "15")
			message(w, 429, "wait")
			return
		}
		reply(w, 200, map[string]string{"checkout_url": "synthetic-" + q.Username})
	}
	s.processPublicLinkQueue(context.Background(), execute)
	s.processPublicLinkQueue(context.Background(), execute)
	if len(calls) != 1 || s.linkQueue.jobs[0].state != "queued" {
		t.Fatal("cooldown retried or jumped queue")
	}
	limited = false
	s.linkQueue.jobs[0].nextAttempt = time.Time{}
	s.processPublicLinkQueue(context.Background(), execute)
	s.processPublicLinkQueue(context.Background(), execute)
	if strings.Join(calls, ",") != "first,first,second" {
		t.Fatal(calls)
	}
	w = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "/", nil)
	r.SetPathValue("ticket", a)
	r.AddCookie(&http.Cookie{Name: "__Host-xgift-link", Value: "owner-a"})
	s.publicLinkQueueStatus(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "synthetic-first") || strings.Contains(w.Body.String(), "synthetic-second") {
		t.Fatal("wrong order returned", w.Body.String())
	}
	if a == enqueue(first, "owner-a") {
		t.Fatal("new submission reused completed ticket")
	}
}

func TestReplacementInvalidatesOldCompletedTickets(t *testing.T) {
	old := &publicLinkJob{state: "done", code: 200, request: manualLinkRequest{Username: "recipient"}, result: []byte(`{"checkout_url":"old"}`)}
	current := &publicLinkJob{state: "done", code: 200, request: manualLinkRequest{Username: "recipient"}, result: []byte(`{"checkout_url":"new"}`)}
	other := &publicLinkJob{state: "done", code: 200, request: manualLinkRequest{Username: "other"}, result: []byte(`{"checkout_url":"other"}`)}
	s := &server{linkQueue: publicLinkQueue{jobs: []*publicLinkJob{old, current, other}}}
	s.invalidateOlderPublicResults("recipient", "new")
	if old.code != 409 || strings.Contains(string(old.result), `"old"`) || current.code != 200 || other.code != 200 {
		t.Fatal("stale link retained or another order changed")
	}
}

func TestPublicQueueCancelDuringProcessingDoesNotRetry(t *testing.T) {
	s := checkFixture(t)
	s.enqueuePublicLink(httptest.NewRecorder(), manualLinkRequest{Username: "first", Months: 3}, "owner")
	id := s.linkQueue.jobs[0].id
	s.processPublicLinkQueue(context.Background(), func(w http.ResponseWriter, _ *http.Request, _ manualLinkRequest, _ string) {
		r := httptest.NewRequest("POST", "/", strings.NewReader("{}"))
		r.SetPathValue("ticket", id)
		r.AddCookie(&http.Cookie{Name: "__Host-xgift-link", Value: "owner"})
		s.cancelPublicLinkQueue(httptest.NewRecorder(), r)
		w.Header().Set("Retry-After", "10")
		message(w, 429, "wait")
	})
	if len(s.linkQueue.jobs) != 0 {
		t.Fatal("cancelled worker retried")
	}
	restarted := &server{vault: s.vault}
	if err := restarted.restorePublicLinkQueue(); err != nil {
		t.Fatal(err)
	}
	if len(restarted.linkQueue.jobs) != 0 {
		t.Fatal("cancelled worker returned after restart")
	}
}

func TestRecoverQueueNeverRevealsAnotherBrowsersTicket(t *testing.T) {
	s := &server{}
	s.enqueuePublicLink(httptest.NewRecorder(), manualLinkRequest{Username: "first", Months: 3}, "owner")
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "__Host-xgift-link", Value: "wrong"})
	w := httptest.NewRecorder()
	s.currentPublicLinkQueue(w, r)
	if w.Code != 404 || strings.Contains(w.Body.String(), s.linkQueue.jobs[0].id) {
		t.Fatal("leaked another browser's queue")
	}
}

func TestDeclinedRetryMovesBehindWaitingUsers(t *testing.T) {
	s := &server{}
	for _, user := range []string{"declined", "second", "third"} {
		s.enqueuePublicLink(httptest.NewRecorder(), manualLinkRequest{Username: user, Months: 3}, "owner-"+user)
	}
	original := s.linkQueue.jobs[0]
	s.processPublicLinkQueue(context.Background(), func(w http.ResponseWriter, r *http.Request, q manualLinkRequest, owner string) {
		w.Header().Set("X-Checkout-Requeue", "declined")
		message(w, 409, "requeue")
	})
	if s.linkQueue.jobs[2] != original || original.state != "queued" {
		t.Fatal("declined retry lost ticket or kept priority")
	}
	var calls []string
	for range 3 {
		s.processPublicLinkQueue(context.Background(), func(w http.ResponseWriter, r *http.Request, q manualLinkRequest, owner string) {
			calls = append(calls, q.Username)
			reply(w, 200, map[string]string{"checkout_url": "synthetic-" + q.Username})
		})
	}
	if strings.Join(calls, ",") != "second,third,declined" {
		t.Fatal("declined user jumped queue", calls)
	}
}

func checkFixture(t *testing.T) *server {
	t.Helper()
	dir := t.TempDir()
	password := filepath.Join(dir, "password")
	if e := os.WriteFile(password, []byte(strings.Repeat("p", 32)), 0600); e != nil {
		t.Fatal(e)
	}
	v, e := vault.Open(filepath.Join(dir, "vault.db"), password, true)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { v.Close() })
	return &server{vault: v, origin: "https://example.test", work: make(chan struct{}, 1), checks: make(chan struct{}, 4), limits: map[string]limit{}}
}
