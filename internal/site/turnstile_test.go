package site

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type turnstileTransport func(*http.Request) (*http.Response, error)

func (f turnstileTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestTurnstileFailsClosedBeforeBusinessHandler(t *testing.T) {
	for _, tc := range []struct {
		name, token, body string
		httpStatus, want  int
		transportError    bool
	}{
		{"missing", "", "", 200, 403, false},
		{"too long", strings.Repeat("x", 2049), "", 200, 403, false},
		{"valid", "synthetic-token", `{"success":true,"hostname":"example.test","action":"manual_link"}`, 200, 204, false},
		{"invalid", "synthetic-token", `{"success":false}`, 200, 403, false},
		{"replayed", "synthetic-token", `{"success":false,"error-codes":["timeout-or-duplicate"]}`, 200, 403, false},
		{"wrong host", "synthetic-token", `{"success":true,"hostname":"other.test","action":"manual_link"}`, 200, 403, false},
		{"wrong action", "synthetic-token", `{"success":true,"hostname":"example.test","action":"redeem"}`, 200, 403, false},
		{"unavailable", "synthetic-token", `{}`, 503, 503, false},
		{"bad JSON", "synthetic-token", `not-json`, 200, 503, false},
		{"network error", "synthetic-token", "", 200, 503, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			requests := 0
			s := &server{origin: "https://example.test", turnstileSiteKey: "configured", turnstileSecret: "synthetic-secret"}
			s.turnstileHTTP = &http.Client{Transport: turnstileTransport(func(r *http.Request) (*http.Response, error) {
				requests++
				if r.URL.String() != turnstileVerifyURL || r.Method != "POST" {
					t.Fatal("incorrect verification destination")
				}
				if err := r.ParseForm(); err != nil {
					t.Fatal(err)
				}
				if r.Form.Get("secret") != s.turnstileSecret || r.Form.Get("response") != tc.token {
					t.Fatal("verification parameters missing")
				}
				if tc.transportError {
					return nil, errors.New("offline")
				}
				return &http.Response{StatusCode: tc.httpStatus, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
			})}
			req := httptest.NewRequest("POST", "/api/manual-link", strings.NewReader(`{}`))
			req.Header.Set("X-Turnstile-Token", tc.token)
			w := httptest.NewRecorder()
			s.human("manual_link", func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(204) })(w, req)
			if w.Code != tc.want || called != (tc.want == 204) {
				t.Fatalf("status %d handler %v", w.Code, called)
			}
			if (tc.token == "" || len(tc.token) > 2048) && requests != 0 {
				t.Fatal("invalid token reached provider")
			}
			if strings.Contains(w.Body.String(), s.turnstileSecret) {
				t.Fatal("secret exposed")
			}
		})
	}
}
func TestTurnstileConfigurationAndPublicKeyOnly(t *testing.T) {
	s := &server{}
	t.Setenv("XGIFT_TURNSTILE_SITE_KEY", "")
	t.Setenv("XGIFT_TURNSTILE_SECRET_FILE", "")
	if err := s.configureTurnstile(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XGIFT_TURNSTILE_SITE_KEY", "configured")
	if err := s.configureTurnstile(); err == nil {
		t.Fatal("partial configuration accepted")
	}
	t.Setenv("XGIFT_TURNSTILE_SECRET_FILE", "/unused")
	t.Setenv("XGIFT_TURNSTILE_SITE_KEY", "1x00000000000000000000AA")
	if err := s.configureTurnstile(); err == nil {
		t.Fatal("production accepted test key")
	}
	s.turnstileSiteKey = "public-site-key"
	s.turnstileSecret = "private-secret"
	w := httptest.NewRecorder()
	s.securityConfig(w, httptest.NewRequest("GET", "/api/security", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "public-site-key") || strings.Contains(w.Body.String(), "private-secret") {
		t.Fatal("unsafe public configuration")
	}
}
