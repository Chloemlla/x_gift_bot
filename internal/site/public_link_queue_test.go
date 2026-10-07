package site

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	s.linkQueue.jobs[0].seen = time.Now().Add(-301 * time.Second)
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
	if ahead, seconds := read(); ahead != 1 || seconds != 220 {
		t.Fatal(ahead, seconds)
	}
	first.started = time.Now().Add(-10 * time.Second)
	first.state = "processing"
	if ahead, seconds := read(); ahead != 1 || seconds != 210 {
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
			w.Header().Set("X-Checkout-Wait-Seconds", "180")
			message(w, 429, "wait")
			return
		}
		reply(w, 200, map[string]string{"status": "succeeded"})
	}
	s.processPublicLinkQueue(context.Background(), execute)
	first, second := s.linkQueue.jobs[0], s.linkQueue.jobs[1]
	if first.nextAttempt.Sub(time.Now()) > 11*time.Second || time.Until(s.linkQueue.blockedUntil) < 179*time.Second {
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
	if state.Seconds < 360 || state.Seconds > 410 {
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
	s.linkQueue.jobs = []*publicLinkJob{{id: "ticket", owner: "owner", state: "done", code: 200, finished: time.Now().Add(-20 * time.Second), request: manualLinkRequest{Username: "holder", Months: 3}, result: []byte(`{"checkout_url":"old","expires_at":` + strconv.FormatInt(created+180, 10) + `}`)}}
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

func TestPublicQueueRestartPreservesTicketsOwnershipAndOrder(t *testing.T) {
	s := checkFixture(t)
	for _, user := range []string{"first", "second", "third"} {
		s.enqueuePublicLink(httptest.NewRecorder(), manualLinkRequest{QueueProtocol: 1, Username: user, Months: 3}, "owner-"+user)
	}
	original := append([]*publicLinkJob(nil), s.linkQueue.jobs...)
	s.linkQueue.jobs[0].state = "processing"
	s.linkQueue.jobs[0].started = time.Now()
	s.linkQueue.jobs[1].seen = time.Now().Add(-2 * time.Minute)
	s.linkQueue.dirty = true
	if err := s.persistPublicLinkQueueLocked(); err != nil {
		t.Fatal(err)
	}
	restarted := &server{vault: s.vault}
	if err := restarted.restorePublicLinkQueue(); err != nil {
		t.Fatal(err)
	}
	if len(restarted.linkQueue.jobs) != 3 {
		t.Fatal("lost tickets")
	}
	for i, j := range restarted.linkQueue.jobs {
		if j.id != original[i].id || j.owner != original[i].owner || j.state != "queued" || time.Since(j.seen) > time.Second {
			t.Fatal("lost order, identity, or reconnect grace")
		}
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.SetPathValue("ticket", original[1].id)
	r.AddCookie(&http.Cookie{Name: "__Host-xgift-link", Value: "owner-second"})
	w := httptest.NewRecorder()
	restarted.publicLinkQueueStatus(w, r)
	var status struct {
		Ticket string
		Ahead  int
	}
	json.Unmarshal(w.Body.Bytes(), &status)
	if w.Code != 202 || status.Ticket != original[1].id || status.Ahead != 1 {
		t.Fatal("old browser could not continue", w.Code)
	}
	// The first resumed job still belongs to the original recipient, and a second
	// restart cannot execute a job whose completion was already persisted.
	var users []string
	execute := func(w http.ResponseWriter, r *http.Request, req manualLinkRequest, owner string) {
		users = append(users, req.Username)
		reply(w, 200, map[string]string{"status": "succeeded"})
	}
	restarted.processPublicLinkQueue(context.Background(), execute)
	again := &server{vault: s.vault}
	if err := again.restorePublicLinkQueue(); err != nil {
		t.Fatal(err)
	}
	again.processPublicLinkQueue(context.Background(), execute)
	if strings.Join(users, ",") != "first,second" {
		t.Fatal("duplicate or reordered execution", users)
	}
}

func TestPublicQueueDoesNotAcknowledgeOrExecuteWithoutDurableStorage(t *testing.T) {
	s := checkFixture(t)
	s.vault.Close()
	w := httptest.NewRecorder()
	s.enqueuePublicLink(w, manualLinkRequest{Username: "first", Months: 3}, "owner")
	if w.Code != 503 {
		t.Fatal("acknowledged unsaved ticket", w.Code)
	}
	s.processPublicLinkQueue(context.Background(), func(http.ResponseWriter, *http.Request, manualLinkRequest, string) {
		t.Fatal("created without durable checkpoint")
	})
}

func TestPublicQueueShutdownKeepsInFlightTicket(t *testing.T) {
	s := checkFixture(t)
	s.enqueuePublicLink(httptest.NewRecorder(), manualLinkRequest{Username: "first", Months: 6}, "owner")
	id := s.linkQueue.jobs[0].id
	ctx, cancel := context.WithCancel(context.Background())
	s.processPublicLinkQueue(ctx, func(w http.ResponseWriter, _ *http.Request, _ manualLinkRequest, _ string) {
		cancel()
		message(w, 502, "cancelled")
	})
	restarted := &server{vault: s.vault}
	if err := restarted.restorePublicLinkQueue(); err != nil {
		t.Fatal(err)
	}
	if len(restarted.linkQueue.jobs) != 1 || restarted.linkQueue.jobs[0].state != "queued" || restarted.linkQueue.jobs[0].id != id {
		t.Fatal("shutdown lost pending ticket")
	}
}

func TestPublicQueueCorruptionFailsClosed(t *testing.T) {
	s := checkFixture(t)
	s.vault.Put(publicQueueKey, []byte(`{"version":2,"jobs":[]}`))
	if s.restorePublicLinkQueue() == nil {
		t.Fatal("unknown snapshot silently discarded")
	}
}

func TestPublicQueueCancellationSurvivesRestartAndChecksOwner(t *testing.T) {
	s := checkFixture(t)
	for _, user := range []string{"first", "second"} {
		s.enqueuePublicLink(httptest.NewRecorder(), manualLinkRequest{Username: user, Months: 3}, user)
	}
	id := s.linkQueue.jobs[0].id
	cancel := func(owner string) int {
		r := httptest.NewRequest("POST", "/", strings.NewReader("{}"))
		r.SetPathValue("ticket", id)
		r.AddCookie(&http.Cookie{Name: "__Host-xgift-link", Value: owner})
		w := httptest.NewRecorder()
		s.cancelPublicLinkQueue(w, r)
		return w.Code
	}
	cancel("second")
	if len(s.linkQueue.jobs) != 2 {
		t.Fatal("another browser cancelled the ticket")
	}
	if cancel("first") != 200 || len(s.linkQueue.jobs) != 1 {
		t.Fatal("cancel did not release place")
	}
	restarted := &server{vault: s.vault}
	if err := restarted.restorePublicLinkQueue(); err != nil {
		t.Fatal(err)
	}
	if len(restarted.linkQueue.jobs) != 1 || restarted.linkQueue.jobs[0].request.Username != "second" {
		t.Fatal("cancelled ticket restored or survivor reordered")
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

func TestPublicQueueRefreshRecoversSameTicketWithoutDispatchWhileAway(t *testing.T) {
	s := checkFixture(t)
	s.enqueuePublicLink(httptest.NewRecorder(), manualLinkRequest{Username: "first", Months: 3}, "owner")
	id := s.linkQueue.jobs[0].id
	r := httptest.NewRequest("POST", "/", nil)
	r.SetPathValue("ticket", id)
	r.AddCookie(&http.Cookie{Name: "__Host-xgift-link", Value: "owner"})
	s.cancelPublicLinkQueue(httptest.NewRecorder(), r) // legacy pagehide beacon
	s.processPublicLinkQueue(context.Background(), func(http.ResponseWriter, *http.Request, manualLinkRequest, string) {
		t.Fatal("created while browser disconnected")
	})
	if len(s.linkQueue.jobs) != 1 || s.linkQueue.jobs[0].cancelled {
		t.Fatal("refresh cancelled ticket")
	}
	r = httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "__Host-xgift-link", Value: "owner"})
	w := httptest.NewRecorder()
	s.currentPublicLinkQueue(w, r)
	var got struct {
		Ticket, Username string
		Months           int
	}
	json.Unmarshal(w.Body.Bytes(), &got)
	if w.Code != 200 || got.Ticket != id || got.Username != "first" || got.Months != 3 || !s.linkQueue.jobs[0].left.IsZero() {
		t.Fatal("failed to resume exact ticket")
	}
	restarted := &server{vault: s.vault}
	if err := restarted.restorePublicLinkQueue(); err != nil {
		t.Fatal(err)
	}
	if len(restarted.linkQueue.jobs) != 1 || restarted.linkQueue.jobs[0].id != id {
		t.Fatal("resumed ticket not durable")
	}
}

func TestDisconnectedQueueWaitsForReconnectAndThenExpires(t *testing.T) {
	s := &server{}
	s.enqueuePublicLink(httptest.NewRecorder(), manualLinkRequest{Username: "first", Months: 3}, "owner")
	job := s.linkQueue.jobs[0]
	job.seen = time.Now().Add(-2 * time.Minute)
	s.processPublicLinkQueue(context.Background(), func(http.ResponseWriter, *http.Request, manualLinkRequest, string) {
		t.Fatal("created for offline browser")
	})
	if len(s.linkQueue.jobs) != 1 {
		t.Fatal("short network interruption lost position")
	}
	job.left = time.Now().Add(-91 * time.Second)
	s.linkQueue.prune(time.Now())
	if len(s.linkQueue.jobs) != 0 {
		t.Fatal("closed page did not leave queue")
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

func TestStoredQueueFailuresAreFinalClientErrors(t *testing.T) {
	s := &server{}
	s.enqueuePublicLink(httptest.NewRecorder(), manualLinkRequest{Username: "first", Months: 3}, "owner")
	id := s.linkQueue.jobs[0].id
	s.processPublicLinkQueue(context.Background(), func(w http.ResponseWriter, _ *http.Request, _ manualLinkRequest, _ string) {
		message(w, 502, "upstream failed")
	})
	r := httptest.NewRequest("GET", "/", nil)
	r.SetPathValue("ticket", id)
	r.AddCookie(&http.Cookie{Name: "__Host-xgift-link", Value: "owner"})
	w := httptest.NewRecorder()
	s.publicLinkQueueStatus(w, r)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "upstream failed") {
		t.Fatal("final failure looks transient", w.Code, w.Body.String())
	}
}

func TestPublicQueueRestoreSkipsInvalidTicketsInsteadOfFailing(t *testing.T) {
	s := checkFixture(t)
	s.enqueuePublicLink(httptest.NewRecorder(), manualLinkRequest{Username: "yearly", Months: 12}, "owner-a")
	s.enqueuePublicLink(httptest.NewRecorder(), manualLinkRequest{Username: "bad", Months: 30}, "owner-b")
	s.linkQueue.dirty = true
	if err := s.persistPublicLinkQueueLocked(); err != nil {
		t.Fatal(err)
	}
	restarted := &server{vault: s.vault}
	if err := restarted.restorePublicLinkQueue(); err != nil {
		t.Fatal(err)
	}
	if len(restarted.linkQueue.jobs) != 1 || restarted.linkQueue.jobs[0].request.Months != 12 {
		t.Fatal("12-month ticket lost or invalid ticket kept", len(restarted.linkQueue.jobs))
	}
}

func TestFinishedTicketsAreRecoveredOnlyDuringTheirPaymentWindow(t *testing.T) {
	s := &server{}
	s.enqueuePublicLink(httptest.NewRecorder(), manualLinkRequest{Username: "first", Months: 3}, "owner")
	job := s.linkQueue.jobs[0]
	job.state, job.code, job.finished = "done", 200, time.Now().Add(-time.Minute)
	current := func() int {
		r := httptest.NewRequest("GET", "/", nil)
		r.AddCookie(&http.Cookie{Name: "__Host-xgift-link", Value: "owner"})
		w := httptest.NewRecorder()
		s.currentPublicLinkQueue(w, r)
		return w.Code
	}
	job.result = []byte(`{"checkout_url":"https://checkout.stripe.com/c/pay/cs_test_x","expires_at":` + strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10) + `}`)
	if current() != 200 {
		t.Fatal("live link not recoverable")
	}
	job.result = []byte(`{"checkout_url":"https://checkout.stripe.com/c/pay/cs_test_x","expires_at":` + strconv.FormatInt(time.Now().Add(-time.Second).Unix(), 10) + `}`)
	if current() != 404 {
		t.Fatal("expired link replayed")
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.SetPathValue("ticket", job.id)
	r.AddCookie(&http.Cookie{Name: "__Host-xgift-link", Value: "owner"})
	w := httptest.NewRecorder()
	s.publicLinkQueueStatus(w, r)
	if w.Code != http.StatusGone {
		t.Fatal("expired ticket not reported as finished", w.Code)
	}
	job.code, job.result = 409, []byte(`{"message":"declined"}`)
	if current() != 404 {
		t.Fatal("failure replayed on revisit")
	}
}
