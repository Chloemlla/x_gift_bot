package site

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func recoveryFixture(t *testing.T, state string) *server {
	t.Helper()
	s := resumeFixture(t, "review", state)
	// Synthetic card used only in an isolated local vault. No network calls.
	if err := s.vault.Put("card", []byte(`{"number":"4242424242424242","exp_month":"12","exp_year":"2099","cvc":"123","billing_name":"Test","email":"test@example.invalid","billing_country":"US"}`)); err != nil {
		t.Fatal(err)
	}
	return s
}
func recoveryRequest(s *server, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/admin/recovery/"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	switch path {
	case "preview":
		s.recoveryPreview(w, req)
	case "start":
		s.recoveryStart(w, req)
	case "stop":
		s.recoveryStop(w, req)
	}
	return w
}

func TestRecoveryStatusSeparatesHistoryFromCurrentOrders(t *testing.T) {
	s := recoveryFixture(t, "created")
	q := &recoveryBatch{ID: "historical-task", State: "completed", Created: 123, Items: []recoveryItem{{ID: "old", State: "succeeded"}}}
	if err := s.saveRecovery(q); err != nil {
		t.Fatal(err)
	}
	before, _ := s.vault.Get("admin-recovery:latest")
	checkoutBefore, _ := s.vault.Get("checkout:1234")
	read := func(wantReview, wantProcessing int) {
		t.Helper()
		w := httptest.NewRecorder()
		s.recoveryStatus(w, httptest.NewRequest("GET", "/api/admin/recovery", nil))
		var result struct {
			Batch   recoveryBatch                    `json:"batch"`
			Summary struct{ Review, Processing int } `json:"summary"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		if result.Batch.State != "completed" || len(result.Batch.Items) != 1 || result.Batch.Items[0].State != "succeeded" {
			t.Fatal("historical result was replaced by current queue")
		}
		if result.Summary.Review != wantReview || result.Summary.Processing != wantProcessing {
			t.Fatal("queue summary did not reflect current database")
		}
	}
	read(1, 0)
	if _, err := s.db.Exec("UPDATE codes SET status='processing'"); err != nil {
		t.Fatal(err)
	}
	read(0, 1)
	if _, err := s.db.Exec("UPDATE codes SET status='succeeded'"); err != nil {
		t.Fatal(err)
	}
	read(0, 0)
	after, _ := s.vault.Get("admin-recovery:latest")
	checkoutAfter, _ := s.vault.Get("checkout:1234")
	if string(before) != string(after) || string(checkoutBefore) != string(checkoutAfter) || len(s.work) != 0 {
		t.Fatal("status lookup mutated history or payment state")
	}
}
func TestRecoveryPreviewDoesNotPayOrExposeSecrets(t *testing.T) {
	s := recoveryFixture(t, "created")
	before, _ := s.vault.Get("checkout:1234")
	w := recoveryRequest(s, "preview", `{}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	after, _ := s.vault.Get("checkout:1234")
	if string(before) != string(after) || len(s.work) != 0 {
		t.Fatal("preview modified checkout or started worker")
	}
	text := w.Body.String()
	for _, secret := range []string{"4242424242424242", "binding", "recipient", "digest", "client_secret", "cs_live_"} {
		if strings.Contains(text, `"`+secret+`":`) || secret == "4242424242424242" && strings.Contains(text, secret) {
			t.Fatalf("response exposed %s", secret)
		}
	}
	q, _ := s.loadRecovery()
	if q.State != "preview" || len(q.Items) != 1 || q.Items[0].State != "pending" {
		t.Fatalf("bad preview: %+v", q)
	}
	if w = recoveryRequest(s, "start", `{"id":"`+q.ID+`","confirm":false}`); w.Code != 409 {
		t.Fatal("missing confirmation accepted")
	}
}
func TestRecoveryStartSynchronizesSuccessOnce(t *testing.T) {
	s := recoveryFixture(t, "succeeded")
	s.payments = false
	if w := recoveryRequest(s, "preview", `{}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	q, _ := s.loadRecovery()
	body := `{"id":"` + q.ID + `","confirm":true}`
	if w := recoveryRequest(s, "start", body); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	s.jobs.Wait()
	q, _ = s.loadRecovery()
	if q.State != "completed" || q.Items[0].State != "succeeded" {
		t.Fatalf("bad result %+v", q)
	}
	c, _ := s.find("XG-" + strings.Repeat("A", 48))
	if c.Status != "succeeded" {
		t.Fatal("success not synchronized")
	}
	if w := recoveryRequest(s, "start", body); w.Code != 200 {
		t.Fatal("duplicate request not idempotent")
	}
	if len(s.work) != 0 {
		t.Fatal("worker lock leaked")
	}
}
func TestRecoveryRejectsStalePlanAndCard(t *testing.T) {
	for _, change := range []string{"order", "card", "expiry"} {
		t.Run(change, func(t *testing.T) {
			s := recoveryFixture(t, "created")
			recoveryRequest(s, "preview", `{}`)
			q, _ := s.loadRecovery()
			switch change {
			case "order":
				s.db.Exec("UPDATE codes SET status='revoked'")
			case "card":
				b, _ := s.vault.Get("card")
				s.vault.Put("card", []byte(strings.ReplaceAll(string(b), "2099", "2098")))
			case "expiry":
				q.Created = time.Now().Add(-11 * time.Minute).Unix()
				s.saveRecovery(q)
			}
			w := recoveryRequest(s, "start", `{"id":"`+q.ID+`","confirm":true}`)
			if w.Code != 409 {
				t.Fatal(w.Body.String())
			}
			if len(s.work) != 0 {
				t.Fatal("worker started for stale preview")
			}
		})
	}
}
func TestRecoveryRestartAndStopNeverRestartPayments(t *testing.T) {
	s := recoveryFixture(t, "unknown")
	recoveryRequest(s, "preview", `{}`)
	q, _ := s.loadRecovery()
	if q.Items[0].State != "skipped" {
		t.Fatal("unknown payment offered for retry")
	}
	q.State = "running"
	q.Items[0].State = "running"
	s.saveRecovery(q)
	if err := s.initRecovery(); err != nil {
		t.Fatal(err)
	}
	q, _ = s.loadRecovery()
	if q.State != "interrupted" || len(s.work) != 0 {
		t.Fatal("restart resumed work")
	}
	q.State = "running"
	s.saveRecovery(q)
	w := recoveryRequest(s, "stop", `{"id":"`+q.ID+`"}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	q, _ = s.loadRecovery()
	if q.State != "stopping" {
		t.Fatal("stop not persisted")
	}
}
func TestRecoveryAuthenticationAndOrigin(t *testing.T) {
	s := recoveryFixture(t, "created")
	s.origin = "https://example.invalid"
	s.adminHash = sha256.Sum256([]byte("test-admin"))
	s.limits = map[string]limit{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/admin/recovery/start", s.admin(s.recoveryStart))
	handler := s.middleware(mux)
	for _, tt := range []struct {
		origin, pass string
		code         int
	}{{s.origin, "", 401}, {"https://other.invalid", "test-admin", 403}} {
		req := httptest.NewRequest("POST", "/api/admin/recovery/start", strings.NewReader(`{}`))
		req.Header.Set("Origin", tt.origin)
		req.Header.Set("Content-Type", "application/json")
		if tt.pass != "" {
			req.SetBasicAuth("admin", tt.pass)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != tt.code {
			t.Fatalf("got %d want %d", w.Code, tt.code)
		}
	}
}
func TestRecoveryBlockedAdviceIsNotQueued(t *testing.T) {
	s := recoveryFixture(t, "declined")
	raw, _ := s.vault.Get("checkout:1234")
	var r map[string]any
	json.Unmarshal(raw, &r)
	r["last_error"] = map[string]any{"HTTP": 402, "Type": "card_error", "AdviceCode": "do_not_try_again"}
	raw, _ = json.Marshal(r)
	s.vault.Put("checkout:1234", raw)
	recoveryRequest(s, "preview", `{}`)
	q, _ := s.loadRecovery()
	if q.Items[0].State != "skipped" {
		t.Fatal("no-retry order queued")
	}
}

func TestRecoveryLinksPreviewWithoutCards(t *testing.T) {
	s := resumeFixture(t, "review", "created")
	if w := recoveryRequest(s, "preview", `{"mode":"links"}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	q, _ := s.loadRecovery()
	if q.Mode != "links" || q.Last4 != "" || q.Binding != "" {
		t.Fatalf("bad links preview: %+v", q)
	}
	if w := recoveryRequest(s, "preview", `{}`); w.Code != 503 {
		t.Fatal("pay preview accepted without cards")
	}
}
