package site

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestPublicQueueAbandonedRequestsExpireWithoutCreatingOrders(t *testing.T) {
	s := &server{}
	s.enqueuePublicLink(httptest.NewRecorder(), manualLinkRequest{Username: "gone", Months: 3}, "owner")
	s.linkQueue.jobs[0].seen = time.Now().Add(-91 * time.Second)
	s.processPublicLinkQueue(context.Background(), func(http.ResponseWriter, *http.Request, manualLinkRequest, string) { t.Fatal("abandoned job executed") })
	if len(s.linkQueue.jobs) != 0 {
		t.Fatal("abandoned job retained")
	}
}

func TestPublicQueueShowsRemainingTimeInsteadOfInternalRate(t *testing.T) {
	first := &publicLinkJob{id: "first", state: "queued"}
	second := &publicLinkJob{id: "second", state: "queued"}
	q := publicLinkQueue{jobs: []*publicLinkJob{first, second}}
	read := func() (int, int) {
		w := httptest.NewRecorder()
		q.respond(w, second)
		var result struct {
			Ahead   int    `json:"ahead"`
			Seconds int    `json:"estimated_wait_seconds"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result.Message, "预计") || strings.Contains(result.Message, "每 15 秒") {
			t.Fatal(result.Message)
		}
		return result.Ahead, result.Seconds
	}
	if ahead, seconds := read(); ahead != 1 || seconds != 40 {
		t.Fatal(ahead, seconds)
	}
	first.started = time.Now().Add(-10 * time.Second)
	first.state = "processing"
	if ahead, seconds := read(); ahead != 1 || seconds != 30 {
		t.Fatal(ahead, seconds)
	}
	first.state = "done"
	if ahead, seconds := read(); ahead != 0 || seconds != 20 {
		t.Fatal(ahead, seconds)
	}
}
