package site

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"xgift/internal/checkout"
	"xgift/internal/vault"
)

func resumeFixture(t *testing.T, status, ledgerStatus string) *server {
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
	db, e := sql.Open("sqlite3", filepath.Join(dir, "site.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	_, e = db.Exec(`CREATE TABLE codes(id TEXT PRIMARY KEY, hash TEXT UNIQUE, hint TEXT, batch TEXT, months INTEGER, status TEXT, username TEXT, recipient_id TEXT UNIQUE, message TEXT, created INTEGER, updated INTEGER, progress INTEGER);`)
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec("INSERT INTO codes VALUES (?,?,?,?,?,?,?,?,?,?,?,?)", "code1", hash("XG-"+strings.Repeat("A", 48)), "AAAA", "batch", 6, status, "recipient", "1234", "original", 123, 124, 50)
	if e != nil {
		t.Fatal(e)
	}
	r := checkout.Record{Username: "recipient", RecipientID: "1234", Months: 6, Amount: 60000, Currency: "BDT", ProductID: "prod_EXAMPLE6MO", SessionID: "cs_live_Original123", URL: "https://checkout.stripe.com/a/pay/cs_live_Original123", Status: ledgerStatus, Created: 123}
	raw, _ := json.Marshal(r)
	if e = v.Put("checkout:1234", raw); e != nil {
		t.Fatal(e)
	}
	return &server{db: db, vault: v, payments: true, lockPath: filepath.Join(dir, "checkout.lock"), work: make(chan struct{}, 1), ctx: context.Background()}
}
func submitResume(s *server, user string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"code": "XG-" + strings.Repeat("A", 48), "username": user})
	req := httptest.NewRequest("POST", "/api/redeem", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.redeem(w, req)
	s.jobs.Wait()
	return w
}
func TestReviewSubmission(t *testing.T) {
	t.Run("reconcile completed original order", func(t *testing.T) {
		s := resumeFixture(t, "review", "succeeded")
		w := submitResume(s, "recipient")
		if w.Code != 202 {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		c, e := s.find("XG-" + strings.Repeat("A", 48))
		if e != nil || c.Status != "succeeded" || c.Progress != 100 || c.Username != "recipient" || c.RecipientID != "1234" {
			t.Fatalf("unexpected code: %+v %v", c, e)
		}
		if len(s.work) != 0 {
			t.Fatal("worker slot leaked")
		}
	})
	t.Run("unpaid order actually rechecked", func(t *testing.T) {
		s := resumeFixture(t, "review", "created")
		w := submitResume(s, "recipient")
		if w.Code != 202 {
			t.Fatalf("status=%d", w.Code)
		}
		c, e := s.find("XG-" + strings.Repeat("A", 48))
		if e != nil || c.Status != "review" || !strings.Contains(c.Message, "重新检查") {
			t.Fatalf("unexpected code: %+v %v", c, e)
		}
	})
	t.Run("wrong user cannot resume", func(t *testing.T) {
		s := resumeFixture(t, "review", "created")
		if w := submitResume(s, "other"); w.Code != 409 {
			t.Fatalf("status=%d", w.Code)
		}
	})
	t.Run("paused cannot resume payment", func(t *testing.T) {
		s := resumeFixture(t, "review", "created")
		s.payments = false
		if w := submitResume(s, "recipient"); w.Code != 503 {
			t.Fatalf("status=%d", w.Code)
		}
		c, _ := s.find("XG-" + strings.Repeat("A", 48))
		if c.Status != "review" {
			t.Fatal("paused order changed")
		}
	})
	t.Run("processing is not resubmitted", func(t *testing.T) {
		s := resumeFixture(t, "processing", "created")
		if w := submitResume(s, "recipient"); w.Code != 200 {
			t.Fatalf("status=%d", w.Code)
		}
		c, _ := s.find("XG-" + strings.Repeat("A", 48))
		if c.Status != "processing" {
			t.Fatal("processing order changed")
		}
	})
	t.Run("cross process lock prevents resume", func(t *testing.T) {
		s := resumeFixture(t, "review", "created")
		lock, e := os.OpenFile(s.lockPath, os.O_CREATE|os.O_RDWR, 0600)
		if e != nil {
			t.Fatal(e)
		}
		defer lock.Close()
		if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
			t.Fatal(e)
		}
		if w := submitResume(s, "recipient"); w.Code != 503 {
			t.Fatalf("status=%d", w.Code)
		}
		if len(s.work) != 0 {
			t.Fatal("worker slot leaked")
		}
	})
	t.Run("status query never resumes unpaid payment", func(t *testing.T) {
		s := resumeFixture(t, "review", "created")
		body, _ := json.Marshal(map[string]string{"code": "XG-" + strings.Repeat("A", 48), "username": "recipient"})
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/status", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		s.status(w, req)
		c, _ := s.find("XG-" + strings.Repeat("A", 48))
		if w.Code != 200 || c.Status != "review" || c.Message != "original" {
			t.Fatalf("query changed order: %+v", c)
		}
	})
}
