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
	if ahead, seconds := read(); ahead != 1 || seconds != 940 {
		t.Fatal(ahead, seconds)
	}
	first.started = time.Now().Add(-10 * time.Second)
	first.state = "processing"
	if ahead, seconds := read(); ahead != 1 || seconds != 930 {
		t.Fatal(ahead, seconds)
	}
	first.state = "done"
	if ahead, seconds := read(); ahead != 0 || seconds != 20 {
		t.Fatal(ahead, seconds)
	}
}

func TestPublicQueuePaymentWindowEstimateAndEarlyRecheck(t *testing.T) {
	s := &server{}
	s.enqueuePublicLink(httptest.NewRecorder(), manualLinkRequest{Username: "first", Months: 3}, "first")
	s.enqueuePublicLink(httptest.NewRecorder(), manualLinkRequest{Username: "second", Months: 3}, "second")
	calls := 0
	execute := func(w http.ResponseWriter, r *http.Request, req manualLinkRequest, owner string) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "10")
			w.Header().Set("X-Checkout-Wait-Seconds", "900")
			message(w, 429, "wait")
			return
		}
		reply(w, 200, map[string]string{"status": "succeeded"})
	}
	s.processPublicLinkQueue(context.Background(), execute)
	first, second := s.linkQueue.jobs[0], s.linkQueue.jobs[1]
	if first.nextAttempt.Sub(time.Now()) > 11*time.Second || time.Until(s.linkQueue.blockedUntil) < 899*time.Second {
		t.Fatal("payment window confused with recheck interval")
	}
	w := httptest.NewRecorder()
	s.linkQueue.respond(w, second)
	var state struct {
		Seconds int `json:"estimated_wait_seconds"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.Seconds < 1800 || state.Seconds > 1850 {
		t.Fatal("ETA omitted payment window", state.Seconds)
	}
	first.nextAttempt = time.Time{}
	s.processPublicLinkQueue(context.Background(), execute)
	if first.state != "done" || !s.linkQueue.blockedUntil.IsZero() {
		t.Fatal("early completion did not release wait")
	}
}

func TestQueuedPlanCanBeChangedWithoutLosingPosition(t *testing.T) {
	s := &server{}
	s.enqueuePublicLink(httptest.NewRecorder(), manualLinkRequest{Username: "recipient", Months: 3}, "browser")
	s.enqueuePublicLink(httptest.NewRecorder(), manualLinkRequest{Username: "other", Months: 3}, "other-browser")
	ticket := s.linkQueue.jobs[0].id
	s.linkQueue.jobs[0].nextAttempt = time.Now().Add(time.Minute)
	w := httptest.NewRecorder()
	s.enqueuePublicLink(w, manualLinkRequest{Username: "recipient", Months: 6}, "browser")
	if w.Code != 202 || len(s.linkQueue.jobs) != 2 || s.linkQueue.jobs[0].id != ticket || s.linkQueue.jobs[0].request.Months != 6 || !s.linkQueue.jobs[0].nextAttempt.IsZero() {
		t.Fatal("plan change lost queue position or remained blocked")
	}
	var got int
	s.processPublicLinkQueue(context.Background(), func(w http.ResponseWriter, r *http.Request, q manualLinkRequest, owner string) {
		got = q.Months
		reply(w, 200, map[string]string{"status": "created"})
	})
	if got != 6 {
		t.Fatal("executed obsolete plan", got)
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

func TestCurrentHolderCanChangePlanAheadOfWaitingAccounts(t *testing.T) {
	s := checkFixture(t)
	created := time.Now().Add(-time.Minute).Unix()
	order := map[string]any{"username": "holder", "recipient_id": "1234", "session_id": "cs_live_Holder", "created": created, "months": 3, "status": "created"}
	active, _ := json.Marshal(map[string]any{"order": order, "expires_at": (created + 900) * 1000})
	public, _ := json.Marshal(map[string]any{"order": order})
	if err := s.vault.Put("checkout-creation:active", active); err != nil {
		t.Fatal(err)
	}
	if err := s.vault.Put("public-checkout:1234", public); err != nil {
		t.Fatal(err)
	}
	s.enqueuePublicLink(httptest.NewRecorder(), manualLinkRequest{Username: "other", Months: 3}, "other")
	s.enqueuePublicLink(httptest.NewRecorder(), manualLinkRequest{Username: "holder", Months: 6}, "holder")
	var user string
	var months int
	s.processPublicLinkQueue(context.Background(), func(w http.ResponseWriter, r *http.Request, q manualLinkRequest, owner string) {
		user, months = q.Username, q.Months
		reply(w, 200, map[string]string{"status": "created"})
	})
	if user != "holder" || months != 6 || s.linkQueue.jobs[0].state != "queued" {
		t.Fatal("holder was blocked by its own payment window", user, months)
	}
}

func TestDelayedLinkPollWaitsForVerificationInsteadOfFailingWhenBusy(t *testing.T) {
	s := checkFixture(t)
	created := time.Now().Add(-time.Minute).Unix()
	order := map[string]any{"username": "holder", "recipient_id": "1234", "session_id": "cs_live_Holder", "created": created, "months": 3, "status": "created"}
	active, _ := json.Marshal(map[string]any{"order": order, "expires_at": (created + 900) * 1000})
	public, _ := json.Marshal(map[string]any{"order": order})
	s.vault.Put("checkout-creation:active", active)
	s.vault.Put("public-checkout:1234", public)
	s.linkQueue.jobs = []*publicLinkJob{{id: "ticket", owner: "owner", state: "done", code: 200, finished: time.Now().Add(-20 * time.Second), request: manualLinkRequest{Username: "holder", Months: 3}, result: []byte(`{"checkout_url":"old"}`)}}
	s.work <- struct{}{}
	r := httptest.NewRequest("GET", "/", nil)
	r.SetPathValue("ticket", "ticket")
	r.AddCookie(&http.Cookie{Name: "__Host-xgift-link", Value: "owner"})
	w := httptest.NewRecorder()
	s.publicLinkQueueStatus(w, r)
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"ticket":"ticket"`) || strings.Contains(w.Body.String(), "checkout_url") {
		t.Fatal("busy verification returned a stale link or an error", w.Code, w.Body.String())
	}
}
