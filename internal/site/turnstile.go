package site

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const turnstileVerifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

func (s *server) configureTurnstile() error {
	s.turnstileSiteKey = strings.TrimSpace(os.Getenv("XGIFT_TURNSTILE_SITE_KEY"))
	path := os.Getenv("XGIFT_TURNSTILE_SECRET_FILE")
	if s.turnstileSiteKey == "" && path == "" {
		return nil
	}
	if s.turnstileSiteKey == "" || path == "" {
		return errors.New("both Turnstile site key and secret file are required")
	}
	// Cloudflare's published test keys must never protect a production origin.
	if strings.HasPrefix(s.turnstileSiteKey, "1x") || strings.HasPrefix(s.turnstileSiteKey, "2x") || strings.HasPrefix(s.turnstileSiteKey, "3x") {
		return errors.New("Turnstile test keys are not permitted")
	}
	b, err := privateFile(path)
	if err != nil {
		return err
	}
	defer clear(b)
	s.turnstileSecret = strings.TrimSpace(string(b))
	if len(s.turnstileSecret) < 20 {
		return errors.New("Turnstile secret is invalid")
	}
	s.turnstileHTTP = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("unexpected Turnstile redirect") }}
	return nil
}
func (s *server) securityConfig(w http.ResponseWriter, r *http.Request) {
	reply(w, 200, map[string]any{"turnstile_enabled": s.turnstileSiteKey != "", "turnstile_site_key": s.turnstileSiteKey})
}
func (s *server) human(action string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.turnstileSiteKey == "" {
			next(w, r)
			return
		}
		token := r.Header.Get("X-Turnstile-Token")
		if len(token) == 0 || len(token) > 2048 {
			message(w, 403, "请完成人机验证后重试。")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		form := url.Values{"secret": {s.turnstileSecret}, "response": {token}}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, turnstileVerifyURL, strings.NewReader(form.Encode()))
		if err != nil {
			message(w, 503, "人机验证暂时不可用，请稍后重试。")
			return
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		client := s.turnstileHTTP
		if client == nil {
			message(w, 503, "人机验证暂时不可用，请稍后重试。")
			return
		}
		res, err := client.Do(req)
		if err != nil {
			message(w, 503, "人机验证暂时不可用，请稍后重试。")
			return
		}
		defer res.Body.Close()
		var result struct {
			Success  bool
			Hostname string
			Action   string
		}
		if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 8192)).Decode(&result) != nil {
			message(w, 503, "人机验证暂时不可用，请稍后重试。")
			return
		}
		origin, err := url.Parse(s.origin)
		if err != nil || !result.Success || result.Hostname != origin.Hostname() || result.Action != action {
			message(w, 403, "人机验证未通过或已过期，请重新验证。")
			return
		}
		next(w, r)
	}
}
