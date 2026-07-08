// Package server wires the HTTP API, static frontend and middleware.
package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Buco7854/lightngx/internal/accounts"
	"github.com/Buco7854/lightngx/internal/auth"
	"github.com/Buco7854/lightngx/internal/confdir"
	"github.com/Buco7854/lightngx/internal/config"
	"github.com/Buco7854/lightngx/internal/logs"
	"github.com/Buco7854/lightngx/internal/nginxctl"
	"github.com/Buco7854/lightngx/internal/sites"
	"github.com/Buco7854/lightngx/internal/store"
	"github.com/Buco7854/lightngx/internal/webauthnx"
)

type Server struct {
	cfg      *config.Config
	sessions *auth.Sessions
	oidc     *auth.OIDC
	limiter  *auth.RateLimiter
	nginx    *nginxctl.Controller
	conf     *confdir.Dir
	logs     *logs.Store
	sites    *sites.Manager
	streams  *sites.Manager
	accounts *accounts.Service
	webauthn *webauthnx.Manager
	static   fs.FS

	// mutate serializes every config mutation with its nginx -t and
	// rollback, so concurrent saves cannot fail on each other's in-flight
	// changes or roll back content another request just committed.
	mutate sync.Mutex

	logStreams atomic.Int32 // active SSE log streams (capped)
}

const maxLogStreams = 32

func contentHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

type ctxKey int

const sessionKey ctxKey = 0

func withSession(ctx context.Context, s *auth.Session) context.Context {
	return context.WithValue(ctx, sessionKey, s)
}

func sessionFrom(ctx context.Context) (*auth.Session, bool) {
	s, ok := ctx.Value(sessionKey).(*auth.Session)
	return s, ok
}

func New(cfg *config.Config, sessions *auth.Sessions, oidcClient *auth.OIDC,
	nginx *nginxctl.Controller, conf *confdir.Dir, logStore *logs.Store,
	siteMgr, streamMgr *sites.Manager, acct *accounts.Service, wa *webauthnx.Manager, static fs.FS) *Server {
	return &Server{
		cfg:      cfg,
		sessions: sessions,
		oidc:     oidcClient,
		limiter:  auth.NewRateLimiter(5, 5*time.Minute),
		nginx:    nginx,
		conf:     conf,
		logs:     logStore,
		sites:    siteMgr,
		streams:  streamMgr,
		accounts: acct,
		webauthn: wa,
		static:   static,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Public: no session required.
	mux.HandleFunc("GET /api/auth/status", s.handleAuthStatus)
	mux.HandleFunc("POST /api/auth/setup", s.handleSetup)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	if s.oidc != nil {
		mux.HandleFunc("GET /api/auth/oidc/login", s.oidc.Begin)
		mux.HandleFunc("GET /api/auth/oidc/callback", s.handleOIDCCallback)
	}

	// Step mux: any valid session (partial or full). Login MFA and
	// enrolment live here so a partial session can complete itself.
	step := http.NewServeMux()
	step.HandleFunc("GET /api/me", s.handleMe)
	step.HandleFunc("POST /api/auth/mfa/verify/totp", s.handleVerifyTOTP)
	step.HandleFunc("POST /api/auth/mfa/verify/webauthn/begin", s.handleVerifyWebAuthnBegin)
	step.HandleFunc("POST /api/auth/mfa/verify/webauthn/finish", s.handleVerifyWebAuthnFinish)
	step.HandleFunc("POST /api/mfa/totp/begin", s.handleTOTPBegin)
	step.HandleFunc("POST /api/mfa/totp/confirm", s.handleTOTPConfirm)
	step.HandleFunc("POST /api/mfa/webauthn/register/begin", s.handleRegisterWebAuthnBegin)
	step.HandleFunc("POST /api/mfa/webauthn/register/finish", s.handleRegisterWebAuthnFinish)

	// Full session required.
	api := http.NewServeMux()
	api.HandleFunc("GET /api/mfa/webauthn", s.handleListCredentials)
	api.HandleFunc("DELETE /api/mfa/webauthn", s.handleDeleteCredential)
	api.HandleFunc("DELETE /api/mfa/totp", s.handleDeleteTOTP)
	api.HandleFunc("POST /api/account/password", s.handleChangePassword)
	api.HandleFunc("GET /api/account/sessions", s.handleListSessions)
	api.HandleFunc("DELETE /api/account/sessions/{sid}", s.handleRevokeSession)
	api.HandleFunc("GET /api/config/tree", s.handleTree)
	api.HandleFunc("GET /api/config/file", s.handleReadFile)
	api.HandleFunc("PUT /api/config/file", s.handleWriteFile)
	api.HandleFunc("DELETE /api/config/file", s.handleDeleteFile)
	api.HandleFunc("POST /api/config/rename", s.handleRenameFile)
	api.HandleFunc("POST /api/config/mkdir", s.handleMkdir)
	api.HandleFunc("GET /api/vhosts/{kind}", s.handleVhostList)
	api.HandleFunc("POST /api/vhosts/{kind}/action", s.handleVhostAction)
	api.HandleFunc("POST /api/vhosts/{kind}/rename", s.handleVhostRename)
	api.HandleFunc("GET /api/logs", s.handleLogList)
	api.HandleFunc("GET /api/logs/read", s.handleLogRead)
	api.HandleFunc("GET /api/logs/stream", s.handleLogStream)

	// Admin-only.
	admin := http.NewServeMux()
	admin.HandleFunc("GET /api/admin/mfa-policy", s.handleGetPolicy)
	admin.HandleFunc("POST /api/admin/mfa-policy", s.handleSetPolicy)
	admin.HandleFunc("GET /api/admin/users", s.handleListUsers)
	admin.HandleFunc("POST /api/admin/users", s.handleCreateUser)
	admin.HandleFunc("PATCH /api/admin/users/{id}", s.handleUpdateUser)
	admin.HandleFunc("POST /api/admin/users/{id}/reset-mfa", s.handleResetUserMFA)
	admin.HandleFunc("DELETE /api/admin/users/{id}", s.handleDeleteUser)
	admin.HandleFunc("GET /api/admin/api-keys", s.handleListAPIKeys)
	admin.HandleFunc("POST /api/admin/api-keys", s.handleCreateAPIKey)
	admin.HandleFunc("DELETE /api/admin/api-keys/{id}", s.handleDeleteAPIKey)
	api.Handle("/api/admin/", s.requireAdmin(admin))

	// nginx control accepts a full session or a scoped API key. Registered
	// on the root mux so these specific patterns take precedence over the
	// session-only /api/ group below.
	mux.Handle("GET /api/nginx/status", s.requireScopeOrSession("nginx:status", s.handleStatus))
	mux.Handle("POST /api/nginx/test", s.requireScopeOrSession("nginx:test", s.handleTest))
	mux.Handle("POST /api/nginx/reload", s.requireScopeOrSession("nginx:reload", s.handleReload))
	mux.Handle("POST /api/nginx/restart", s.requireScopeOrSession("nginx:restart", s.handleRestart))

	// Order matters: most specific prefixes first.
	mux.Handle("/api/me", s.requireStep(step))
	mux.Handle("/api/auth/mfa/", s.requireStep(step))
	mux.Handle("/api/mfa/totp/begin", s.requireStep(step))
	mux.Handle("/api/mfa/totp/confirm", s.requireStep(step))
	mux.Handle("/api/mfa/webauthn/register/", s.requireStep(step))
	mux.Handle("/api/", s.requireAuth(api))

	mux.Handle("/", s.staticHandler())

	return securityHeaders(csrfProtect(mux))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(w http.ResponseWriter, r *http.Request, v any, maxBytes int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return false
	}
	return true
}

// ---- auth ----

// issueSession issues a session cookie for a local user at the given level.
func (s *Server) issueSession(w http.ResponseWriter, r *http.Request, u store.User, method, level string) error {
	sid, err := s.accounts.Store().CreateSession(u.ID, level, method, r.UserAgent(), s.clientIP(r), s.sessions.TTLFor(level))
	if err != nil {
		return err
	}
	return s.sessions.Issue(w, auth.Session{
		UserID: u.ID, User: u.Username, Role: u.Role, Method: method, Level: level, Sid: sid,
	})
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	boot, err := s.accounts.NeedsBootstrap()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "storage error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"bootstrap": boot,
		"local":     true,
		"oidc":      s.cfg.OIDCEnabled(),
		"oidcLabel": s.cfg.OIDCLabel,
	})
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if !s.limiter.Allow(ip) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many attempts, try again later"})
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req, 4096) {
		return
	}
	if err := accounts.ValidatePassword(req.Password); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	u, err := s.accounts.Bootstrap(req.Username, req.Password)
	if err != nil {
		s.limiter.Fail(ip)
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	// If the policy already requires MFA for this role (e.g. pinned via
	// LN_MFA_REQUIRED_ROLES), the new admin must enrol a second factor
	// straight away, same as the login flow. Otherwise they are fully in
	// (and, when the policy is undecided, prompted to choose it in-app).
	level := auth.LevelFull
	if required, err := s.accounts.RoleRequiresMFA(u.Role); err == nil && required {
		level = auth.LevelEnroll
	}
	if err := s.issueSession(w, r, u, "local", level); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "session error"})
		return
	}
	s.audit(r, "setup.admin_created", "username", u.Username, "level", level)
	writeJSON(w, http.StatusOK, map[string]string{"user": u.Username, "level": level})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req, 4096) {
		return
	}
	// Throttle per IP and per account, so a shared proxy IP can't lock
	// everyone out and a distributed guess at one account is still bounded.
	userKey := "user:" + strings.ToLower(req.Username)
	if !s.limiter.Allow(ip) || !s.limiter.Allow(userKey) {
		s.audit(r, "login.ratelimited", "username", req.Username)
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many attempts, try again later"})
		return
	}
	dec, err := s.accounts.Authenticate(req.Username, req.Password)
	if err != nil {
		s.limiter.Fail(ip)
		s.limiter.Fail(userKey)
		s.audit(r, "login.failed", "username", req.Username)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}
	s.limiter.Reset(ip)
	s.limiter.Reset(userKey)
	level := dec.Level
	// A remembered device skips the pending second factor.
	if level == auth.LevelMFA && s.trustedDevice(r, dec.User.ID) {
		level = auth.LevelFull
		s.audit(r, "login.mfa_skipped_trusted", "username", dec.User.Username)
	}
	if err := s.issueSession(w, r, dec.User, "local", level); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "session error"})
		return
	}
	s.audit(r, "login.success", "username", dec.User.Username, "level", level)
	writeJSON(w, http.StatusOK, map[string]string{"user": dec.User.Username, "level": level})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if sess, err := s.sessions.FromRequest(r); err == nil && sess.Sid != "" {
		_ = s.accounts.Store().DeleteSession(sess.Sid, sess.UserID)
	}
	s.sessions.Clear(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	username, isAdmin, err := s.oidc.Callback(w, r)
	if err != nil {
		s.audit(r, "oidc.failed", "error", err.Error())
		http.Error(w, "OIDC login failed", http.StatusForbidden)
		return
	}
	// OIDC role comes from the admin-mapping env; OIDC bypasses local MFA.
	role := "user"
	if isAdmin {
		role = "admin"
	}
	sess := auth.Session{User: username, Role: role, Method: "oidc", Level: auth.LevelFull}
	if err := s.sessions.Issue(w, sess); err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	s.audit(r, "oidc.success", "username", username, "role", role)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r.Context())
	resp := map[string]any{
		"user":      sess.User,
		"role":      sess.Role,
		"method":    sess.Method,
		"level":     sess.Level,
		"expiresAt": sess.ExpiresAt,
	}
	// Local users: report enrolled factors so the client can render the
	// right verify/enrol screen. OIDC users carry no local record.
	if sess.Method == "local" {
		if u, err := s.accounts.Store().GetUser(sess.UserID); err == nil {
			resp["mfa"] = map[string]bool{"totp": u.TOTPEnrolled, "webauthn": u.WebAuthnCount > 0}
		}
		required, _ := s.accounts.RoleRequiresMFA(sess.Role)
		resp["mfaRequired"] = required
	}
	// Admins learn whether they still owe an MFA-policy decision.
	if sess.IsAdmin() && sess.Level == auth.LevelFull {
		if p, err := s.accounts.Policy(); err == nil {
			resp["policy"] = p
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// ---- config files ----

func (s *Server) handleTree(w http.ResponseWriter, r *http.Request) {
	tree, err := s.conf.Tree()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"root": s.conf.Root(), "tree": tree})
}

func (s *Server) handleReadFile(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	content, err := s.conf.Read(path)
	if err != nil {
		writeJSON(w, statusFor(err), map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"path": path, "content": string(content), "hash": contentHash(content)})
}

func (s *Server) handleWriteFile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path     string `json:"path"`
		Content  string `json:"content"`
		BaseHash string `json:"baseHash"`
	}
	if !readJSON(w, r, &req, s.cfg.MaxEditSize+64<<10) {
		return
	}
	s.mutate.Lock()
	defer s.mutate.Unlock()
	// Reject a save based on a version that is no longer on disk, so two
	// editors cannot silently overwrite each other. A save without
	// baseHash overwrites unconditionally.
	if req.BaseHash != "" {
		current, err := s.conf.Read(req.Path)
		if errors.Is(err, fs.ErrNotExist) {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": "file no longer exists (deleted or renamed since it was opened)",
				"gone":  true})
			return
		}
		if err != nil || contentHash(current) != req.BaseHash {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "file changed on disk since it was opened"})
			return
		}
	}
	restore, err := s.conf.Write(req.Path, []byte(req.Content))
	if err != nil {
		writeJSON(w, statusFor(err), map[string]string{"error": err.Error()})
		return
	}
	out, err := s.nginx.Test(r.Context())
	if err != nil {
		if rerr := restore(); rerr != nil {
			s.audit(r, "config.write.rollback_failed", "path", req.Path, "error", rerr.Error())
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": "config test failed AND rollback failed, fix manually", "output": out})
			return
		}
		s.audit(r, "config.write.rejected", "path", req.Path)
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": "nginx config test failed, changes rolled back", "output": out})
		return
	}
	s.audit(r, "config.write", "path", req.Path)
	resp := map[string]any{
		"status": "saved", "output": out, "hash": contentHash([]byte(req.Content))}
	s.applyReload(r, "config.write", resp)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleMkdir(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if !readJSON(w, r, &req, 4096) {
		return
	}
	s.mutate.Lock()
	defer s.mutate.Unlock()
	if err := s.conf.Mkdir(req.Path); err != nil {
		writeJSON(w, statusFor(err), map[string]string{"error": err.Error()})
		return
	}
	s.audit(r, "config.mkdir", "path", req.Path)
	writeJSON(w, http.StatusOK, map[string]string{"status": "created"})
}

func (s *Server) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	s.mutate.Lock()
	defer s.mutate.Unlock()
	restore, err := s.conf.Delete(path)
	if err != nil {
		writeJSON(w, statusFor(err), map[string]string{"error": err.Error()})
		return
	}
	out, err := s.nginx.Test(r.Context())
	if err != nil {
		if rerr := restore(); rerr != nil {
			s.audit(r, "config.delete.rollback_failed", "path", path, "error", rerr.Error())
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": "config test failed AND rollback failed, fix manually", "output": out})
			return
		}
		s.audit(r, "config.delete.rejected", "path", path)
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": "nginx config test failed, deletion rolled back", "output": out})
		return
	}
	s.audit(r, "config.delete", "path", path)
	resp := map[string]any{"status": "deleted", "output": out}
	s.applyReload(r, "config.delete", resp)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleRenameFile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if !readJSON(w, r, &req, 4096) {
		return
	}
	s.mutate.Lock()
	defer s.mutate.Unlock()
	restore, err := s.conf.Rename(req.From, req.To)
	if err != nil {
		writeJSON(w, statusFor(err), map[string]string{"error": err.Error()})
		return
	}
	out, err := s.nginx.Test(r.Context())
	if err != nil {
		if rerr := restore(); rerr != nil {
			s.audit(r, "config.rename.rollback_failed", "from", req.From, "error", rerr.Error())
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": "config test failed AND rollback failed, fix manually", "output": out})
			return
		}
		s.audit(r, "config.rename.rejected", "from", req.From, "to", req.To)
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": "nginx config test failed, rename rolled back", "output": out})
		return
	}
	s.audit(r, "config.rename", "from", req.From, "to", req.To)
	resp := map[string]any{"status": "renamed", "output": out}
	s.applyReload(r, "config.rename", resp)
	writeJSON(w, http.StatusOK, resp)
}

// applyReload reloads nginx after a mutation passed nginx -t, when
// LN_AUTO_RELOAD is on, recording the outcome in resp. The change is already
// saved and valid, so a reload failure is surfaced but not treated as fatal.
func (s *Server) applyReload(r *http.Request, action string, resp map[string]any) {
	if !s.cfg.AutoReload {
		return
	}
	if _, err := s.nginx.Reload(r.Context()); err != nil {
		s.audit(r, action+".reload_failed", "error", err.Error())
		resp["reloadError"] = err.Error()
		return
	}
	resp["reloaded"] = true
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, confdir.ErrOutsideRoot):
		return http.StatusForbidden
	case errors.Is(err, confdir.ErrTooLarge), errors.Is(err, confdir.ErrBinary):
		return http.StatusUnprocessableEntity
	case errors.Is(err, fs.ErrNotExist):
		return http.StatusNotFound
	case errors.Is(err, logs.ErrNotAllowed):
		return http.StatusForbidden
	default:
		return http.StatusInternalServerError
	}
}

// ---- nginx control ----

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, map[string]any{
		"running":   s.nginx.Running(),
		"version":   s.nginx.Version(ctx),
		"supervise": s.cfg.Supervise,
	})
}

func (s *Server) handleTest(w http.ResponseWriter, r *http.Request) {
	out, err := s.nginx.Test(r.Context())
	s.audit(r, "nginx.test", "ok", err == nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": err == nil, "output": out})
}

func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	out, err := s.nginx.Reload(r.Context())
	s.audit(r, "nginx.reload", "ok", err == nil)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": err.Error(), "output": out})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "reloaded", "output": out})
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	out, err := s.nginx.Restart(r.Context())
	s.audit(r, "nginx.restart", "ok", err == nil)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": err.Error(), "output": out})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "restarted", "output": out})
}

// ---- logs ----

func (s *Server) handleLogList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"files": s.logs.List()})
}

func (s *Server) handleLogRead(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	before := parseInt64(q.Get("before"), 0)
	maxBytes := parseInt64(q.Get("bytes"), 64<<10)
	if maxBytes <= 0 || maxBytes > 1<<20 {
		maxBytes = 64 << 10
	}
	chunk, err := s.logs.ReadTail(q.Get("path"), before, maxBytes)
	if err != nil {
		writeJSON(w, statusFor(err), map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, chunk)
}

func (s *Server) handleLogStream(w http.ResponseWriter, r *http.Request) {
	if s.logStreams.Add(1) > maxLogStreams {
		s.logStreams.Add(-1)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "too many active log streams"})
		return
	}
	defer s.logStreams.Add(-1)

	q := r.URL.Query()
	path := q.Get("path")
	from := parseInt64(q.Get("from"), -1)
	if from < 0 {
		// Default: start at the current end of file.
		chunk, err := s.logs.ReadTail(path, 0, 1)
		if err != nil {
			writeJSON(w, statusFor(err), map[string]string{"error": err.Error()})
			return
		}
		from = chunk.Size
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	var wmu sync.Mutex
	write := func(s string) error {
		wmu.Lock()
		defer wmu.Unlock()
		if _, err := io.WriteString(w, s); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	// Heartbeat keeps intermediaries from closing the stream.
	go func() {
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if write(": ping\n\n") != nil {
					cancel()
					return
				}
			}
		}
	}()

	_ = s.logs.Follow(ctx, path, from, func(lines []string) error {
		var b strings.Builder
		for _, line := range lines {
			data, _ := json.Marshal(line)
			b.WriteString("data: ")
			b.Write(data)
			b.WriteString("\n\n")
		}
		return write(b.String())
	})
}

func parseInt64(s string, def int64) int64 {
	if s == "" {
		return def
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return def
	}
	return n
}
