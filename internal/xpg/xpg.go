// Package xpg serves a standalone gift link generator: it checks the recipient
// account first, then creates a Stripe checkout link for the recipient to pay.
// It never tokenizes or confirms a payment card.
package xpg

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	box "github.com/sagernet/sing-box"

	"xgift/internal/proxy"
	"xgift/internal/vault"
)

//go:embed assets
var assetsFS embed.FS

type Config struct {
	Origin       string
	Listen       string
	DataDir      string
	PasswordFile string
	Passcode     string
}

// LoadConfig reads the service configuration from the environment. The
// passcode file is optional; without it the API accepts all visitors.
func LoadConfig() (Config, error) {
	cfg := Config{Listen: "127.0.0.1:8788"}
	cfg.Origin = strings.TrimRight(os.Getenv("XPG_ORIGIN"), "/")
	u, err := url.Parse(cfg.Origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return cfg, errors.New("XPG_ORIGIN must be an HTTPS origin")
	}
	listen := os.Getenv("XPG_LISTEN")
	if listen != "" {
		cfg.Listen = listen
	}
	host, _, err := net.SplitHostPort(cfg.Listen)
	if err != nil || host == "" {
		return cfg, errors.New("XPG_LISTEN must include a port")
	}
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return cfg, errors.New("XPG_LISTEN must listen on the loopback interface only")
	}
	cfg.DataDir = os.Getenv("XPG_DATA_DIR")
	if cfg.DataDir == "" {
		return cfg, errors.New("XPG_DATA_DIR is required")
	}
	cfg.PasswordFile = os.Getenv("XPG_PASSWORD_FILE")
	if cfg.PasswordFile == "" {
		return cfg, errors.New("XPG_PASSWORD_FILE is required")
	}
	if f := os.Getenv("XPG_PASSCODE_FILE"); f != "" {
		raw, err := os.ReadFile(f)
		if err != nil {
			return cfg, errors.New("XPG_PASSCODE_FILE is unreadable")
		}
		cfg.Passcode = strings.TrimSpace(string(raw))
		if len(cfg.Passcode) < 16 {
			return cfg, errors.New("passcode file must contain at least 16 characters")
		}
	}
	return cfg, nil
}

func lockDataDir(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "xpg.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another xpg instance is using this data directory")
	}
	return f, nil
}

type server struct {
	vault     *vault.Vault
	proxyPort int
	proxyBox  *box.Box
	passcode  string
	dataDir   string
	job       chan struct{}
	limiter   *limiter
	live      int64
	jobs      sync.WaitGroup
}

// newServer assembles the service. proxyBox may be nil in tests.
func newServer(cfg Config, v *vault.Vault, proxyPort int, instance *box.Box) *server {
	return &server{
		vault:     v,
		proxyPort: proxyPort,
		proxyBox:  instance,
		passcode:  cfg.Passcode,
		dataDir:   cfg.DataDir,
		job:       make(chan struct{}, 1),
		limiter:   newLimiter(),
	}
}

// Run boots the standalone link service and blocks until ctx is canceled.
func Run(ctx context.Context) error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	lock, err := lockDataDir(cfg.DataDir)
	if err != nil {
		return err
	}
	defer lock.Close()

	v, err := vault.Open(filepath.Join(cfg.DataDir, "vault.db"), cfg.PasswordFile, false)
	if err != nil {
		return err
	}
	defer v.Close()

	raw, err := v.Get("proxy")
	if err != nil {
		return errors.New("proxy record is missing; run setup or put --name proxy")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	proxyPort := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	instance, err := proxy.Start(ctx, raw, proxyPort)
	clear(raw)
	if err != nil {
		return err
	}
	defer instance.Close()

	s := newServer(cfg, v, proxyPort, instance)
	srv := &http.Server{
		Handler:           s.routes(),
		ReadTimeout:       30 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	fmt.Fprintf(os.Stderr, "xpg: listening on %s (origin %s)\n", cfg.Listen, cfg.Origin)
	select {
	case err = <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			instance.Close()
			return err
		}
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}
	instance.Close()
	v.Close()
	return nil
}

func (s *server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.asset("index.html", "text/html; charset=utf-8", s.ownerCookie))
	mux.HandleFunc("GET /favicon.svg", s.asset("favicon.svg", "image/svg+xml", nil))
	mux.HandleFunc("GET /robots.txt", s.asset("robots.txt", "text/plain; charset=utf-8", nil))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]any{"ok": true, "service": "xpg"})
	})
	mux.HandleFunc("GET /api/plans", s.plans)
	mux.HandleFunc("POST /api/check", s.check)
	mux.HandleFunc("POST /api/link", s.link)
	return mux
}

// asset serves an embedded file. extra, when set, may add response headers
// (for example the browser owner cookie) before the body is written.
func (s *server) asset(name, contentType string, extra func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	body, err := assetsFS.ReadFile("assets/" + name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "xpg: missing embedded asset %s\n", name)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-store")
		if extra != nil {
			extra(w, r)
		}
		_, _ = w.Write(body)
	}
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func replyError(w http.ResponseWriter, status int, code, message string) {
	reply(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

// authorized validates the shared passcode when one is configured. The
// comparison only reports equality, never which characters matched.
func (s *server) authorized(r *http.Request) bool {
	if s.passcode == "" {
		return true
	}
	key := r.Header.Get("X-XPG-Key")
	return key != "" && subtle.ConstantTimeCompare([]byte(key), []byte(s.passcode)) == 1
}

// clientIP prefers the reverse proxy's X-Real-IP (Caddy copies the verified
// CF-Connecting-IP) and falls back to the TCP peer.
func clientIP(r *http.Request) string {
	if v := strings.TrimSpace(strings.Split(r.Header.Get("X-Real-IP"), ",")[0]); v != "" {
		return v
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

const ownerCookie = "xpg_owner"

// ownerCookie issues a stable per-browser identifier cookie used to attribute
// link ownership. Existing 64-hex values are kept.
func (s *server) ownerCookie(w http.ResponseWriter, r *http.Request) {
	requestOwner(w, r)
}

func requestOwner(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(ownerCookie); err == nil && validOwnerValue(c.Value) {
		return c.Value
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return ""
	}
	value := hex.EncodeToString(raw)
	http.SetCookie(w, &http.Cookie{
		Name:     ownerCookie,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   31536000,
	})
	return value
}

func validOwnerValue(v string) bool {
	if len(v) != 64 {
		return false
	}
	_, err := hex.DecodeString(v)
	return err == nil
}

type limiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func newLimiter() *limiter { return &limiter{hits: map[string][]time.Time{}} }

// allow keeps at most max events per key inside the sliding window.
func (l *limiter) allow(key string, window time.Duration, max int) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.hits) > 4096 {
		for k, v := range l.hits {
			if len(v) == 0 || now.Sub(v[len(v)-1]) > window {
				delete(l.hits, k)
			}
		}
	}
	kept := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if now.Sub(t) < window {
			kept = append(kept, t)
		}
	}
	if len(kept) >= max {
		l.hits[key] = kept
		return false
	}
	l.hits[key] = append(kept, now)
	return true
}
