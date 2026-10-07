package site

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"xgift/internal/checkout"
	"xgift/internal/proxy"
	"xgift/internal/vault"
)

//go:embed assets/*
var assets embed.FS

type nonceContextKey struct{}

var usernamePattern = regexp.MustCompile(`^[a-z0-9_]{1,15}$`)
var codePattern = regexp.MustCompile(`^XG-[A-F0-9]{48}$`)

type server struct {
	linkQueue        publicLinkQueue
	turnstileSiteKey string
	turnstileSecret  string
	turnstileHTTP    *http.Client
	db               *sql.DB
	vault            *vault.Vault
	origin           string
	adminHash        [32]byte
	payments         bool
	port             int
	lockPath         string
	work             chan struct{}
	checks           chan struct{}
	jobs             sync.WaitGroup
	ctx              context.Context
	recoveryMu       sync.Mutex
	limitsMu         sync.Mutex
	limits           map[string]limit
}
type limit struct {
	start time.Time
	count int
}
type codeRow struct {
	Copyable    bool   `json:"copyable"`
	Folder      string `json:"folder"`
	ID          string `json:"id"`
	Hint        string `json:"hint"`
	Batch       string `json:"batch"`
	Months      int    `json:"months"`
	Status      string `json:"status"`
	Progress    int    `json:"progress"`
	RecipientID string `json:"-"`
	Username    string `json:"username"`
	Message     string `json:"message"`
	Created     int64  `json:"created"`
	Updated     int64  `json:"updated"`
}

func Run(ctx context.Context) error {
	ctx, cancelService := context.WithCancel(ctx)
	defer cancelService()
	origin := os.Getenv("XGIFT_ORIGIN")
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return errors.New("XGIFT_ORIGIN must be an HTTPS origin")
	}
	dir := os.Getenv("XGIFT_DATA_DIR")
	if dir == "" {
		return errors.New("XGIFT_DATA_DIR is required")
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err = os.Chmod(dir, 0700); err != nil {
		return err
	}
	instance, err := os.OpenFile(filepath.Join(dir, "site.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer instance.Close()
	if err = syscall.Flock(int(instance.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("another site instance is using this data directory")
	}
	admin, err := privateFile(os.Getenv("XGIFT_ADMIN_PASSWORD_FILE"))
	if err != nil {
		return err
	}
	admin = []byte(strings.TrimSpace(string(admin)))
	if len(admin) < 32 {
		return errors.New("admin password must contain at least 32 characters")
	}
	s := &server{origin: origin, adminHash: sha256.Sum256(admin), payments: os.Getenv("XGIFT_PAYMENTS_ENABLED") == "true", lockPath: filepath.Join(dir, "checkout.lock"), work: make(chan struct{}, 1), checks: make(chan struct{}, 4), ctx: ctx, limits: map[string]limit{}}
	clear(admin)
	if err = s.configureTurnstile(); err != nil {
		return err
	}
	v, err := vault.Open(filepath.Join(dir, "vault.db"), os.Getenv("XGIFT_PASSWORD_FILE"), false)
	if err != nil {
		return err
	}
	s.vault = v
	if err = s.initRecovery(); err != nil {
		v.Close()
		return err
	}
	defer v.Close()
	if err = s.restorePublicLinkQueue(); err != nil {
		return err
	}
	if s.payments {
		if err = checkout.CheckPaymentConfiguration(v); err != nil {
			return err
		}
	}
	dbpath := filepath.Join(dir, "site.db")
	f, err := os.OpenFile(dbpath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	f.Close()
	if err = os.Chmod(dbpath, 0600); err != nil {
		return err
	}
	db, err := sql.Open("sqlite3", dbpath+"?_busy_timeout=5000&_journal_mode=WAL&_synchronous=FULL&_foreign_keys=on")
	if err != nil {
		return err
	}
	s.db = db
	defer db.Close()
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS codes (
 id TEXT PRIMARY KEY, hash TEXT NOT NULL UNIQUE, hint TEXT NOT NULL, batch TEXT NOT NULL,
 months INTEGER NOT NULL CHECK(months IN (3,6)),
 status TEXT NOT NULL CHECK(status IN ('active','processing','succeeded','review','revoked')),
 username TEXT NOT NULL DEFAULT '', recipient_id TEXT UNIQUE,
 message TEXT NOT NULL DEFAULT '', created INTEGER NOT NULL, updated INTEGER NOT NULL
 ); CREATE INDEX IF NOT EXISTS codes_created ON codes(created);`)
	if err != nil {
		return err
	}
	// Inspect the schema so upgrades preserve all existing redemption codes.
	columns, err := db.Query("PRAGMA table_info(codes)")
	if err != nil {
		return err
	}
	hasProgress := false
	for columns.Next() {
		var cid, required, primary int
		var name, typ string
		var defaultValue any
		if err = columns.Scan(&cid, &name, &typ, &required, &defaultValue, &primary); err != nil {
			columns.Close()
			return err
		}
		if name == "progress" {
			hasProgress = true
		}
	}
	err = columns.Err()
	columns.Close()
	if err != nil {
		return err
	}
	if !hasProgress {
		if _, err = db.Exec("ALTER TABLE codes ADD COLUMN progress INTEGER NOT NULL DEFAULT 0"); err != nil {
			return err
		}
	}
	if err = migrateFolders(db); err != nil {
		return err
	}
	if err = migrateBatches(db); err != nil {
		return err
	}
	// A crash is never interpreted as permission to submit the same payment again.
	if _, err = db.Exec("UPDATE codes SET status='review',message=?,updated=? WHERE status='processing'", "订单处理被中断，请查询原订单或联系管理员核实；请勿重复兑换。", time.Now().Unix()); err != nil {
		return err
	}
	raw, err := v.Get("proxy")
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	s.port = listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	box, err := proxy.Start(ctx, raw, s.port)
	clear(raw)
	if err != nil {
		return err
	}
	defer box.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.asset("index.html", "text/html; charset=utf-8"))
	mux.HandleFunc("GET /favicon.svg", s.asset("favicon.svg", "image/svg+xml"))
	mux.HandleFunc("GET /appearance.js", s.asset("appearance.js", "application/javascript; charset=utf-8"))
	mux.HandleFunc("GET /payment-notifications.js", s.asset("payment-notifications.js", "application/javascript; charset=utf-8"))
	mux.HandleFunc("GET /app.js", s.asset("app.js", "application/javascript; charset=utf-8"))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if s.db.PingContext(r.Context()) != nil {
			reply(w, 503, map[string]any{"ok": false})
			return
		}
		ready, err := s.paymentsAvailable()
		if err != nil {
			reply(w, 503, map[string]any{"ok": false, "payments_enabled": false})
			return
		}
		reply(w, 200, map[string]any{"ok": true, "payments_enabled": ready})
	})
	mux.HandleFunc("GET /api/security", s.securityConfig)
	mux.HandleFunc("POST /api/redeem", s.human("redeem", s.redeem))
	mux.HandleFunc("GET /api/manual-link/plans", s.publicLinkPlans)
	mux.HandleFunc("POST /api/manual-link", s.human("manual_link", s.publicLink))
	mux.HandleFunc("GET /api/manual-link/queue", s.publicLinkQueueSummary)
	mux.HandleFunc("GET /api/manual-link/queue/current", s.currentPublicLinkQueue)
	mux.HandleFunc("POST /api/manual-link/queue/{ticket}/leave", s.leavePublicLinkQueue)
	mux.HandleFunc("GET /api/manual-link/queue/{ticket}", s.publicLinkQueueStatus)
	mux.HandleFunc("POST /api/manual-link/queue/{ticket}/cancel", s.cancelPublicLinkQueue)
	mux.HandleFunc("POST /api/status", s.status)
	mux.HandleFunc("POST /api/check", s.human("check", s.check))
	mux.HandleFunc("GET /admin", s.admin(s.asset("admin.html", "text/html; charset=utf-8")))
	mux.HandleFunc("GET /admin.js", s.admin(s.asset("admin.js", "application/javascript; charset=utf-8")))
	mux.HandleFunc("GET /api/admin/codes", s.admin(s.list))
	mux.HandleFunc("POST /api/admin/lookup", s.admin(s.lookup))
	mux.HandleFunc("GET /api/admin/stats", s.admin(s.stats))
	mux.HandleFunc("GET /api/admin/customer", s.admin(s.customerOrder))
	mux.HandleFunc("GET /api/admin/manual-link/plans", s.admin(s.manualLinkPlans))
	mux.HandleFunc("POST /api/admin/manual-link", s.admin(s.manualLink))
	mux.HandleFunc("GET /api/admin/recovery", s.admin(s.recoveryStatus))
	mux.HandleFunc("POST /api/admin/recovery/preview", s.admin(s.recoveryPreview))
	mux.HandleFunc("POST /api/admin/recovery/start", s.admin(s.recoveryStart))
	mux.HandleFunc("POST /api/admin/recovery/stop", s.admin(s.recoveryStop))
	mux.HandleFunc("POST /api/admin/codes", s.admin(s.generate))
	mux.HandleFunc("POST /api/admin/revoke", s.admin(s.revoke))
	mux.HandleFunc("POST /api/admin/folders", s.admin(s.createFolder))
	mux.HandleFunc("POST /api/admin/folders/rename", s.admin(s.renameFolder))
	mux.HandleFunc("POST /api/admin/folders/delete", s.admin(s.deleteFolder))
	mux.HandleFunc("POST /api/admin/codes/move", s.admin(s.moveCodes))
	mux.HandleFunc("POST /api/admin/codes/copy", s.admin(s.copyCode))
	addr := os.Getenv("XGIFT_LISTEN")
	if addr == "" {
		addr = "127.0.0.1:8787"
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("listen address must use a loopback IP")
	}
	h := &http.Server{Addr: addr, Handler: s.middleware(mux), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 50 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan error, 1)
	go func() { done <- h.ListenAndServe() }()
	log.Printf("xgift-web listening on %s; payments enabled=%t", addr, s.payments)
	s.jobs.Add(1)
	go func() { defer s.jobs.Done(); s.reconcileLoop() }()
	s.jobs.Add(1)
	go func() { defer s.jobs.Done(); s.publicLinkQueueLoop() }()
	select {
	case err = <-done:
	case <-ctx.Done():
	}
	cancelService()
	shutdown, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	if e := h.Shutdown(shutdown); e != nil {
		h.Close()
	}
	s.jobs.Wait()
	s.linkQueue.mu.Lock()
	queueErr := s.persistPublicLinkQueueLocked()
	s.linkQueue.mu.Unlock()
	if queueErr != nil {
		return queueErr
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func privateFile(path string) ([]byte, error) {
	i, e := os.Lstat(path)
	if e != nil {
		return nil, e
	}
	if !i.Mode().IsRegular() || i.Mode().Perm()&0077 != 0 {
		return nil, errors.New("secret file must be regular and owner-only")
	}
	return os.ReadFile(path)
}
func token(n int) string {
	b := make([]byte, n)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func hash(code string) string { b := sha256.Sum256([]byte(code)); return hex.EncodeToString(b[:]) }
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func message(w http.ResponseWriter, status int, msg string) {
	reply(w, status, map[string]any{"message": msg})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Header.Get("Content-Type") != "application/json" {
		message(w, 415, "请使用 JSON 请求。")
		return false
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		message(w, 400, "请求格式不正确。")
		return false
	}
	if d.Decode(new(any)) != io.EOF {
		message(w, 400, "请求格式不正确。")
		return false
	}
	return true
}

// An explicit gzip;q=0 takes precedence over a wildcard.
func acceptsGzip(header string) bool {
	wildcard := false
	for _, entry := range strings.Split(header, ",") {
		parts := strings.Split(strings.TrimSpace(entry), ";")
		name := strings.ToLower(strings.TrimSpace(parts[0]))
		quality := 1.0
		for _, parameter := range parts[1:] {
			key, value, found := strings.Cut(strings.TrimSpace(parameter), "=")
			if found && strings.EqualFold(key, "q") {
				parsed, err := strconv.ParseFloat(value, 64)
				if err != nil || parsed < 0 || parsed > 1 {
					quality = 0
				} else {
					quality = parsed
				}
			}
		}
		if name == "gzip" {
			return quality > 0
		}
		if name == "*" {
			wildcard = quality > 0
		}
	}
	return wildcard
}

func (s *server) asset(name, kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := assets.ReadFile("assets/" + name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(kind, "text/html") {
			nonce, _ := r.Context().Value(nonceContextKey{}).(string)
			b = bytes.ReplaceAll(b, []byte("__XGIFT_NONCE__"), []byte(nonce))
		}
		if strings.HasSuffix(name, ".js") {
			w.Header().Set("Vary", "Accept-Encoding")
			if acceptsGzip(r.Header.Get("Accept-Encoding")) {
				if compressed, err := assets.ReadFile("assets/" + name + ".gz"); err == nil {
					b = compressed
					w.Header().Set("Content-Encoding", "gzip")
				}
			}
		}
		w.Header().Set("Content-Type", kind)
		w.Write(b)
	}
}
func (s *server) allow(key string, max int) bool {
	s.limitsMu.Lock()
	defer s.limitsMu.Unlock()
	now := time.Now()
	if len(s.limits) > 10000 {
		for k, v := range s.limits {
			if now.Sub(v.start) > time.Minute {
				delete(s.limits, k)
			}
		}
		if len(s.limits) > 10000 {
			return false
		}
	}
	l := s.limits[key]
	if now.Sub(l.start) > time.Minute {
		l = limit{start: now}
	}
	l.count++
	s.limits[key] = l
	return l.count <= max
}
func (s *server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		nonce := token(16)
		r = r.WithContext(context.WithValue(r.Context(), nonceContextKey{}, nonce))
		// Emotion style elements use a fresh nonce. MUI also sets dynamic style attributes.
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self' https://challenges.cloudflare.com; frame-src https://challenges.cloudflare.com; style-src 'self' 'nonce-"+nonce+"'; style-src-attr 'unsafe-inline'; connect-src 'self'; worker-src 'self'; img-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if r.Method == "POST" && r.Header.Get("Origin") != s.origin {
			message(w, 403, "请求来源不正确，请从本站页面重试。")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/admin") {
			// Caddy overwrites X-Real-IP, and this server accepts loopback traffic only.
			ip := r.Header.Get("X-Real-IP")
			if net.ParseIP(ip) == nil {
				ip, _, _ = net.SplitHostPort(r.RemoteAddr)
			}
			max := 60
			bucket := "api:"
			if r.URL.Path == "/api/redeem" {
				max = 8
				bucket = "redeem:"
			} else if r.URL.Path == "/api/manual-link" {
				max = 4
				bucket = "manual-link:"
			} else if r.URL.Path == "/api/check" {
				max = 8
				bucket = "check:"
			}
			if !s.allow(bucket+ip, max) {
				w.Header().Set("Retry-After", "60")
				message(w, 429, "操作太频繁，请稍后重试。")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
func (s *server) admin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		sum := sha256.Sum256([]byte(password))
		if !ok || subtle.ConstantTimeCompare(sum[:], s.adminHash[:]) != 1 || user != "admin" {
			w.Header().Set("WWW-Authenticate", `Basic realm="XGift Admin", charset="UTF-8"`)
			message(w, 401, "需要管理员登录。")
			return
		}
		next(w, r)
	}
}
func (s *server) find(code string) (codeRow, error) {
	var c codeRow
	e := s.db.QueryRow("SELECT id,hint,batch,months,status,username,message,created,updated,progress,COALESCE(recipient_id,'') FROM codes WHERE hash=?", hash(code)).Scan(&c.ID, &c.Hint, &c.Batch, &c.Months, &c.Status, &c.Username, &c.Message, &c.Created, &c.Updated, &c.Progress, &c.RecipientID)
	return c, e
}
func readInput(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	var q struct {
		Code     string `json:"code"`
		Username string `json:"username"`
	}
	if !decode(w, r, &q) {
		return "", "", false
	}
	q.Code = strings.ToUpper(strings.TrimSpace(q.Code))
	q.Username = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(q.Username), "@"))
	if !codePattern.MatchString(q.Code) || !usernamePattern.MatchString(q.Username) {
		message(w, 400, "请填写完整兑换码和正确的 X 用户名（不是显示名称）。")
		return "", "", false
	}
	return q.Code, q.Username, true
}
func (s *server) status(w http.ResponseWriter, r *http.Request) {
	code, user, ok := readInput(w, r)
	if !ok {
		return
	}
	c, e := s.find(code)
	if e != nil || c.Username != "" && c.Username != user {
		message(w, 404, "兑换码或用户名不匹配。")
		return
	}
	if c.Status == "review" {
		s.reconcileStatus(r.Context(), &c)
	}
	reply(w, 200, map[string]any{"status": c.Status, "months": c.Months, "message": c.Message, "progress": c.Progress, "rechecking": s.autoChecking(&c), "payment_declined": s.paymentDeclined(&c)})
}

// eligibilityCheck is the read-only X pre-check; tests substitute a fake.
var eligibilityCheck = checkout.Eligibility

// check is a read-only eligibility probe: no code lookup, no checkout, no writes.
// It stays available while payments are paused or running. Read-only checks
// have their own bounded concurrency and never occupy the payment worker.
func (s *server) check(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Username string `json:"username"`
	}
	if !decode(w, r, &q) {
		return
	}
	q.Username = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(q.Username), "@"))
	if !usernamePattern.MatchString(q.Username) {
		message(w, 400, "请填写正确的 X 用户名（不是显示名称）。")
		return
	}
	select {
	case s.checks <- struct{}{}:
		defer func() { <-s.checks }()
	default:
		message(w, 503, "当前检测人数较多，请稍后重试检测。")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	_, e := eligibilityCheck(ctx, s.vault, q.Username, s.port)
	if e != nil {
		switch {
		case errors.Is(e, checkout.ErrNotEligible):
			reply(w, 200, map[string]any{"eligible": false, "message": "X 当前不允许向这个账号赠送 Premium。"})
		case errors.Is(e, checkout.ErrUserNotFound):
			reply(w, 200, map[string]any{"eligible": false, "message": "未能找到这个 X 账号，请检查用户名。"})
		default:
			message(w, 503, "暂时无法向 X 核实赠送资格，请稍后重试检测。")
		}
		return
	}
	reply(w, 200, map[string]any{"eligible": true, "message": "该账号当前可以接收赠送。"})
}
func (s *server) redeem(w http.ResponseWriter, r *http.Request) {
	ready, availabilityErr := s.paymentsAvailable()
	if !ready || availabilityErr != nil {
		reply(w, http.StatusServiceUnavailable, map[string]any{"status": "paused", "message": "充值暂时暂停，恢复时间待定。请保留兑换码，已有订单可继续查询进度。"})
		return
	}
	code, user, ok := readInput(w, r)
	if !ok {
		return
	}
	c, e := s.find(code)
	if errors.Is(e, sql.ErrNoRows) {
		message(w, 404, "兑换码不存在，请检查后重试。")
		return
	}
	if e != nil {
		message(w, 503, "服务暂时不可用，兑换码未使用。")
		return
	}
	resuming := c.Status == "review" && c.Username == user && c.RecipientID != ""
	if c.Status != "active" && !resuming {
		if c.Username == user && (c.Status == "processing" || c.Status == "review" || c.Status == "succeeded") {
			reply(w, 200, map[string]any{"status": c.Status, "message": c.Message, "months": c.Months, "progress": c.Progress, "rechecking": s.autoChecking(&c)})
			return
		}
		message(w, 409, "兑换码已使用或已停用，请联系提供方。")
		return
	}
	select {
	case s.work <- struct{}{}:
	default:
		message(w, 503, "正在处理其他请求，请稍后重试。")
		return
	}
	handedOff := false
	defer func() {
		if !handedOff {
			<-s.work
		}
	}()
	recipient := c.RecipientID
	if !resuming {
		ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
		defer cancel()
		recipient, e = checkout.Eligibility(ctx, s.vault, user, s.port)
		if e != nil {
			switch {
			case errors.Is(e, checkout.ErrNotEligible):
				message(w, 422, "X 当前不允许向这个账号赠送 Premium。兑换码未使用，可换一个符合条件的账号。")
			case errors.Is(e, checkout.ErrUserNotFound):
				message(w, 422, "未能找到这个 X 账号，请检查用户名。兑换码未使用。")
			default:
				message(w, 503, "暂时无法向 X 核实赠送资格，请稍后重试。兑换码未使用。")
			}
			return
		}
	}
	// The same lock is used by the CLI. Keep it until the final database write.
	lock, e := os.OpenFile(s.lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		message(w, 503, "订单服务暂时不可用，请稍后重试。")
		return
	}
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		lock.Close()
		message(w, 503, "订单处理中，请稍后重试。")
		return
	}
	release := func() { syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); lock.Close() }
	if !resuming {
		// A prior CLI order must not fulfill a newly presented redemption code.
		for _, key := range []string{"checkout:" + recipient, "checkout:" + user} {
			if _, e = s.vault.Get(key); !errors.Is(e, sql.ErrNoRows) {
				release()
				message(w, 409, "这个账号已有订单记录，需要管理员核实后处理。兑换码未使用。")
				return
			}
		}
	}
	var result sql.Result
	if resuming {
		result, e = s.db.Exec("UPDATE codes SET status='processing',progress=20,message=?,updated=? WHERE id=? AND status='review' AND username=? AND recipient_id=? AND months=?", "正在重新检查原订单，请稍候。", time.Now().Unix(), c.ID, user, recipient, c.Months)
	} else {
		result, e = s.db.Exec("UPDATE codes SET status='processing',progress=20,username=?,recipient_id=?,message=?,updated=? WHERE id=? AND status='active'", user, recipient, "正在处理，请不要重复提交。", time.Now().Unix(), c.ID)
	}
	if e != nil {
		release()
		message(w, 409, "无法开始处理订单，请刷新后查询兑换状态。")
		return
	}
	n, e := result.RowsAffected()
	if e != nil || n != 1 {
		release()
		message(w, 409, "兑换码状态已改变，请刷新后查询。")
		return
	}
	handedOff = true
	s.jobs.Add(1)
	go func() {
		defer s.jobs.Done()
		defer func() { <-s.work }()
		defer release()
		ctx, cancel := context.WithTimeout(s.ctx, 240*time.Second)
		defer cancel()
		ctx = checkout.WithProgress(ctx, func(percent int, msg string) {
			if _, err := s.db.Exec("UPDATE codes SET progress=MAX(progress,?),message=?,updated=? WHERE id=? AND status='processing'", percent, msg, time.Now().Unix(), c.ID); err != nil {
				log.Printf("order %s progress could not be saved", c.ID)
			}
		})
		var record *checkout.Record
		var err error
		if resuming {
			record, err = checkout.ResumeForRecipient(ctx, s.vault, user, recipient, s.port, c.Months)
		} else {
			record, err = checkout.RunForRecipient(ctx, s.vault, user, recipient, true, s.port, c.Months)
		}
		if err != nil {
			stage := "before_order"
			if record != nil {
				stage = record.Status
			}
			failure, marshalErr := json.Marshal(map[string]any{"code_id": c.ID, "recipient_id": recipient, "months": c.Months, "stage": stage, "error": err.Error(), "observed_at": time.Now().Unix()})
			if marshalErr != nil || s.vault.Put(fmt.Sprintf("redemption-failure:%s:%d", c.ID, time.Now().UnixNano()), failure) != nil {
				log.Printf("order %s failure details could not be persisted", c.ID)
			}
			clear(failure)
			log.Printf("order %s stopped at stage %s; upstream details remain encrypted", c.ID, stage)
		}
		status, msg := "review", "订单尚未完成，请联系管理员核实处理阶段；请勿重复兑换。"
		if record != nil && record.SubmittedAt != 0 {
			msg = "付款结果正在自动核实，请保留本页等待；系统不会重复扣款。"
		} else if record != nil && record.ConfirmParameters == "" && record.ConfirmKey == "" {
			if record.Status == "creating" {
				msg = "创建赠送订单未完成，尚未提交付款。请联系管理员处理，请勿重复兑换。"
			} else if record.Status == "created" {
				msg = "订单已创建，尚未提交付款。可以重新检查并继续兑换。"
			}
		}
		if errors.Is(err, checkout.ErrXReadFailure) {
			msg = "暂时无法向 X 核实账号或套餐，本次未提交付款。请稍后点击「重新检查并继续兑换」。"
		} else if errors.Is(err, checkout.ErrNotEligible) {
			msg = "X 当前不允许该账号接收 Premium 赠送，本次未提交付款。账号符合条件后，可重新检查并继续兑换。"
		} else if errors.Is(err, checkout.ErrUserNotFound) {
			msg = "未找到绑定的 X 账号，本次未提交付款。请核对原账号后重新检查。"
		}
		if err == nil && record != nil && record.Status == "succeeded" && record.RecipientID == recipient && record.Months == c.Months {
			plan, planErr := s.catalogPlan(c.Months)
			if planErr == nil && record.Amount == plan.Minor && record.Currency == strings.ToUpper(plan.Currency) {
				status, msg = "succeeded", fmt.Sprintf("已为 @%s 完成 %d 个月 Premium 赠送。", user, c.Months)
			}
		}
		if record != nil && record.Status == "requires_action" {
			msg = "付款需要持卡人完成银行验证，请联系管理员。请勿重复兑换。"
		}
		if checkout.IsPaymentDeclined(record) {
			msg = "付款被支付机构拒绝，本次兑换未完成。请联系管理员处理，请勿重复提交。"
		}
		if errors.Is(err, checkout.ErrPaymentPaused) {
			msg = "充值已自动暂停，原订单已保留。请联系管理员处理付款方式。"
		}
		updated, e := s.db.Exec("UPDATE codes SET status=?,message=?,updated=?,progress=CASE WHEN ?='succeeded' THEN 100 ELSE progress END WHERE id=? AND status='processing'", status, msg, time.Now().Unix(), status, c.ID)
		if e != nil {
			log.Printf("order %s requires database reconciliation", c.ID)
			return
		}
		n, e := updated.RowsAffected()
		if e != nil || n != 1 {
			log.Printf("order %s requires database reconciliation", c.ID)
			return
		}
		// Never log the checkout URL, credentials or upstream payloads.
		log.Printf("order %s finished: %s", c.ID, status)
	}()
	reply(w, 202, map[string]any{"status": "processing", "progress": 20, "months": c.Months, "message": "正在处理，请保留本页并等待结果。"})
}
func (s *server) generate(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Folder string `json:"folder"`
		Months int    `json:"months"`
		Count  int    `json:"count"`
		Batch  string `json:"batch"`
	}
	if !decode(w, r, &q) {
		return
	}
	q.Batch = strings.TrimSpace(q.Batch)
	if (q.Months != 3 && q.Months != 6) || q.Count < 1 || q.Count > 500 || len(q.Batch) > 120 || (q.Folder != "" && !folderIDPattern.MatchString(q.Folder)) {
		message(w, 400, "请选择 3 或 6 个月，数量 1–500，批次名称不超过 120 字节。")
		return
	}
	if q.Batch == "" {
		q.Batch = time.Now().UTC().Format("20060102-150405") + "-" + token(3)
	}
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		message(w, 503, "暂时无法生成兑换码。")
		return
	}
	defer tx.Rollback()
	folder, e := ensureBatch(tx, q.Batch)
	if e != nil {
		message(w, 503, "无法保存批次。")
		return
	}
	if e = tx.QueryRow("SELECT name FROM folders WHERE id=?", folder).Scan(&q.Batch); e != nil {
		message(w, 503, "无法读取批次。")
		return
	}
	codes := make([]string, 0, q.Count)
	now := time.Now().Unix()
	for i := 0; i < q.Count; i++ {
		code := "XG-" + strings.ToUpper(token(24))
		id := token(16)
		// Persist encrypted content first; an interrupted transaction can only leave an
		// unreachable vault record, never an active code without its encrypted value.
		if e = s.vault.Put("redemption:"+id, []byte(code)); e != nil {
			message(w, 503, "无法加密保存兑换码，尚未生成本批。")
			return
		}
		_, e = tx.Exec("INSERT INTO codes(id,hash,hint,batch,months,status,created,updated,folder_id,copyable) VALUES(?,?,?,?,?,'active',?,?,?,1)", id, hash(code), code[len(code)-8:], q.Batch, q.Months, now, now, folder)
		if e != nil {
			message(w, 503, "生成失败，没有保存本批兑换码。")
			return
		}
		codes = append(codes, code)
	}
	if e = tx.Commit(); e != nil {
		message(w, 503, "保存结果不确定，请在后台核实批次后再操作。")
		return
	}
	reply(w, 201, map[string]any{"codes": codes, "batch": q.Batch, "months": q.Months, "folder": folder})
}
func (s *server) revoke(w http.ResponseWriter, r *http.Request) {
	var q struct {
		ID string `json:"id"`
	}
	if !decode(w, r, &q) {
		return
	}
	res, e := s.db.Exec("UPDATE codes SET status='revoked',message='兑换码已停用',updated=? WHERE id=? AND status='active'", time.Now().Unix(), q.ID)
	if e != nil {
		message(w, 503, "停用失败。")
		return
	}
	n, e := res.RowsAffected()
	if e != nil || n != 1 {
		message(w, 409, "只能停用尚未使用的兑换码。")
		return
	}
	message(w, 200, "兑换码已停用。")
}
