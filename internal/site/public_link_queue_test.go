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

func linkReady(user string) linkOutcome {
	return linkOutcome{status: 200, body: map[string]any{"checkout_url": "synthetic-" + user, "expires_at": time.Now().Add(time.Minute).Unix()}}
}

func TestPublicQueueFIFOOwnershipDeduplicationAndRateWait(t *testing.T) {
	s := checkFixture(t)
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
	status := func(owner string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/api/manual-link/queue/"+a, nil)
		r.SetPathValue("ticket", a)
		r.AddCookie(&http.Cookie{Name: "__Host-xgift-link", Value: owner})
		s.publicLinkQueueStatus(w, r)
		return w
	}
	if status("owner-b").Code != 404 {
		t.Fatal("another browser read queue ticket")
	}
	var calls []string
	limited := true
	create := func(_ context.Context, q manualLinkRequest, _ string) linkOutcome {
		calls = append(calls, q.Username)
		if limited {
			o := failed(429, "wait")
			o.retryIn, o.blocked = 15*time.Second, 15*time.Second
			return o
		}
		return linkReady(q.Username)
	}
	s.processPublicLinkQueue(context.Background(), create)
	s.processPublicLinkQueue(context.Background(), create)
	if len(calls) != 1 || s.linkQueue.jobs[0].State != "queued" {
		t.Fatal("cooldown retried or jumped queue")
	}
	limited = false
	s.linkQueue.jobs[0].NextAttempt = time.Time{}
	s.processPublicLinkQueue(context.Background(), create)
	s.processPublicLinkQueue(context.Background(), create)
	if strings.Join(calls, ",") != "first,first,second" {
		t.Fatal(calls)
	}
	if w := status("owner-a"); w.Code != 200 || !strings.Contains(w.Body.String(), "synthetic-first") || strings.Contains(w.Body.String(), "synthetic-second") {
		t.Fatal("wrong order returned", w.Body.String())
	}
	if a == enqueue(first, "owner-a") {
		t.Fatal("new submission reused completed ticket")
	}
}

func TestReplacementInvalidatesOldCompletedTickets(t *testing.T) {
	old := &publicLinkJob{State: "done", Code: 200, Request: manualLinkRequest{Username: "recipient"}, Result: []byte(`{"checkout_url":"old"}`)}
	current := &publicLinkJob{State: "done", Code: 200, Request: manualLinkRequest{Username: "recipient"}, Result: []byte(`{"checkout_url":"new"}`)}
	other := &publicLinkJob{State: "done", Code: 200, Request: manualLinkRequest{Username: "other"}, Result: []byte(`{"checkout_url":"other"}`)}
	s := checkFixture(t)
	s.linkQueue.jobs = []*publicLinkJob{old, current, other}
	s.invalidateOlderPublicResults("recipient", "new")
	if old.Code != 409 || strings.Contains(string(old.Result), `"old"`) || current.Code != 200 || other.Code != 200 {
		t.Fatal("stale link retained or another order changed")
	}
}

func TestPublicQueueCancelDuringProcessingDoesNotRetry(t *testing.T) {
	s := checkFixture(t)
	s.enqueuePublicLink(httptest.NewRecorder(), manualLinkRequest{Username: "first", Months: 3}, "owner")
	id := s.linkQueue.jobs[0].ID
	s.processPublicLinkQueue(context.Background(), func(context.Context, manualLinkRequest, string) linkOutcome {
		r := httptest.NewRequest("POST", "/", strings.NewReader("{}"))
		r.SetPathValue("ticket", id)
		r.AddCookie(&http.Cookie{Name: "__Host-xgift-link", Value: "owner"})
		s.cancelPublicLinkQueue(httptest.NewRecorder(), r)
		o := failed(429, "wait")
		o.retryIn = 10 * time.Second
		return o
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
	s := checkFixture(t)
	s.enqueuePublicLink(httptest.NewRecorder(), manualLinkRequest{Username: "first", Months: 3}, "owner")
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "__Host-xgift-link", Value: "wrong"})
	w := httptest.NewRecorder()
	s.currentPublicLinkQueue(w, r)
	if w.Code != 404 || strings.Contains(w.Body.String(), s.linkQueue.jobs[0].ID) {
		t.Fatal("leaked another browser's queue")
	}
}

func TestDeclinedRetryMovesBehindWaitingUsers(t *testing.T) {
	s := checkFixture(t)
	for _, user := range []string{"declined", "second", "third"} {
		s.enqueuePublicLink(httptest.NewRecorder(), manualLinkRequest{Username: user, Months: 3}, "owner-"+user)
	}
	original := s.linkQueue.jobs[0]
	s.processPublicLinkQueue(context.Background(), func(context.Context, manualLinkRequest, string) linkOutcome {
		o := failed(409, "requeue")
		o.declined = true
		return o
	})
	if s.linkQueue.jobs[2] != original || original.State != "queued" {
		t.Fatal("declined retry lost ticket or kept priority")
	}
	var calls []string
	for range 3 {
		s.processPublicLinkQueue(context.Background(), func(_ context.Context, q manualLinkRequest, _ string) linkOutcome {
			calls = append(calls, q.Username)
			return linkReady(q.Username)
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
