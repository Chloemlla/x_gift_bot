package checkout

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"xgift/internal/vault"
)

type mockXTransport func(*http.Request) (*http.Response, error)

func (f mockXTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestXQuery403Recovery(t *testing.T) {
	dir := t.TempDir()
	pw := filepath.Join(dir, "password")
	if e := os.WriteFile(pw, []byte(strings.Repeat("p", 32)), 0600); e != nil {
		t.Fatal(e)
	}
	v, e := vault.Open(filepath.Join(dir, "vault.db"), pw, true)
	if e != nil {
		t.Fatal(e)
	}
	defer v.Close()
	tests := []struct {
		name       string
		statuses   []int
		mutation   bool
		retryAfter string
		wantCalls  int
		wantError  bool
	}{
		{"recovers", []int{403, 200}, false, "", 2, false},
		{"bounded", []int{403, 403, 403, 200}, false, "", 3, true},
		{"creation forbidden is not retried", []int{403, 200}, true, "", 1, true},
		{"authentication denied is not retried", []int{401, 200}, false, "", 1, true},
		{"long retry after stops", []int{403, 200}, false, "61", 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			c := &xClient{vault: v, headers: http.Header{}, http: &http.Client{Transport: mockXTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "x.com" {
					t.Fatalf("unexpected host %s", r.URL.Host)
				}
				code := tt.statuses[calls]
				calls++
				body := `{"data":{}}`
				if code != 200 {
					body = `{"error":"fixture forbidden"}`
				}
				return &http.Response{StatusCode: code, Header: http.Header{"Retry-After": {tt.retryAfter}, "Content-Type": {"application/json"}, "X-Request-Id": {"fixture-request"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}}
			c.regionalHTTP = c.http
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			var out json.RawMessage
			err := c.call(ctx, "fixture", "PremiumGiftingQuery", "fixture", map[string]string{"screenName": "fixture"}, tt.mutation, &out)
			if (err != nil) != tt.wantError || calls != tt.wantCalls {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
			if tt.wantError && !tt.mutation && !errors.Is(err, ErrXReadFailure) {
				t.Fatalf("missing query failure classification: %v", err)
			}
		})
	}
	// A successful HTTP response saying ineligible is a business result, not retryable.
	calls := 0
	c := &xClient{vault: v, headers: http.Header{}, http: &http.Client{Transport: mockXTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"data":{"user":{"result":{"rest_id":"1234","premium_gifting_eligible":false,"core":{"screen_name":"fixture"}}}}}`))}, nil
	})}}
	_, e = c.recipient(context.Background(), "fixture")
	if !errors.Is(e, ErrNotEligible) || calls != 1 {
		t.Fatalf("ineligible result retried: %d %v", calls, e)
	}
	// The general Stripe/mutation HTTP policy must remain unchanged.
	var retry *temporaryError
	if errors.As(httpFailure(errors.New("forbidden"), 403, ""), &retry) {
		t.Fatal("general 403 policy was broadened")
	}
	db, e := sql.Open("sqlite3", "file:"+filepath.Join(dir, "vault.db")+"?mode=ro")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	rows, e := db.Query("SELECT name FROM secrets WHERE name LIKE 'x-read-failure:%'")
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var name string
		if e = rows.Scan(&name); e != nil {
			t.Fatal(e)
		}
		raw, err := v.Get(name)
		if err != nil {
			t.Fatal(err)
		}
		var audit struct {
			HTTP      int    `json:"http_status"`
			Body      string `json:"body"`
			RequestID string `json:"request_id"`
		}
		if json.Unmarshal(raw, &audit) != nil || (audit.HTTP != 403 && audit.HTTP != 401) || !strings.Contains(audit.Body, "fixture forbidden") || audit.RequestID != "fixture-request" {
			t.Fatal("failed query diagnostic was not preserved")
		}
		count++
	}
	if rows.Err() != nil || count != 6 {
		t.Fatalf("saved failures=%d err=%v", count, rows.Err())
	}
}
