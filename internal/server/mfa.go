package server

import (
	"encoding/json"
	"net/http"

	"github.com/Buco7854/lightngx/internal/accounts"
	"github.com/Buco7854/lightngx/internal/auth"
	"github.com/Buco7854/lightngx/internal/store"
	"github.com/Buco7854/lightngx/internal/webauthnx"
)

// localSession returns the session if it is a local account (MFA only
// applies to local accounts; OIDC bypasses it), else writes an error.
func (s *Server) localSession(w http.ResponseWriter, r *http.Request) (*auth.Session, bool) {
	sess, _ := sessionFrom(r.Context())
	if sess.Method != "local" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "MFA does not apply to this account"})
		return nil, false
	}
	return sess, true
}

// upgradeAfterMFA raises the current session to full once a partial
// (mfa/enroll) session has satisfied its requirement, keeping the same sid.
func (s *Server) upgradeAfterMFA(w http.ResponseWriter, r *http.Request, sess *auth.Session) error {
	u, err := s.accounts.Store().GetUser(sess.UserID)
	if err != nil {
		return err
	}
	if err := s.accounts.Store().UpgradeSession(sess.Sid, auth.LevelFull, s.sessions.TTLFor(auth.LevelFull)); err != nil {
		return err
	}
	return s.sessions.Issue(w, r, auth.Session{
		UserID: u.ID, User: u.Username, Role: u.Role, Method: "local", Level: auth.LevelFull, Sid: sess.Sid,
	})
}

// ---- login-time verification (LevelMFA -> LevelFull) ----

func (s *Server) handleVerifyTOTP(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.localSession(w, r)
	if !ok {
		return
	}
	if sess.Level != auth.LevelMFA {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "no verification pending"})
		return
	}
	ip := s.clientIP(r)
	if !s.limiter.Allow(ip) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many attempts, try again later"})
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if !readJSON(w, r, &req, 1024) {
		return
	}
	secret, confirmed, err := s.accounts.Store().TOTPSecret(sess.UserID)
	lastUsed, _ := s.accounts.Store().TOTPLastUsed(sess.UserID)
	matched, ok := auth.VerifyTOTP(secret, req.Code, lastUsed)
	if err != nil || !confirmed || !ok {
		s.limiter.Fail(ip)
		s.audit(r, "mfa.totp.verify_failed", "username", sess.User)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid code"})
		return
	}
	_ = s.accounts.Store().SetTOTPLastUsed(sess.UserID, matched)
	s.limiter.Reset(ip)
	if err := s.upgradeAfterMFA(w, r, sess); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "session error"})
		return
	}
	if r.URL.Query().Get("remember") == "true" {
		s.setTrustCookie(w, r, sess.UserID)
	}
	s.audit(r, "mfa.totp.verified", "username", sess.User)
	writeJSON(w, http.StatusOK, map[string]string{"level": auth.LevelFull})
}

const (
	waCookieLogin = "ln_wa_login"
	waCookieReg   = "ln_wa_reg"
)

func (s *Server) setWACookie(w http.ResponseWriter, r *http.Request, name string, sd *webauthnx.SessionData) error {
	payload, err := json.Marshal(sd)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: s.sessions.Sign(payload), Path: "/api", MaxAge: 300,
		HttpOnly: true, Secure: s.sessions.Secure(r), SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (s *Server) readWACookie(w http.ResponseWriter, r *http.Request, name string) (*webauthnx.SessionData, bool) {
	c, err := r.Cookie(name)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no challenge in progress"})
		return nil, false
	}
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/api", MaxAge: -1,
		HttpOnly: true, Secure: s.sessions.Secure(r), SameSite: http.SameSiteLaxMode})
	payload, err := s.sessions.Unsign(c.Value)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad challenge"})
		return nil, false
	}
	var sd webauthnx.SessionData
	if err := json.Unmarshal(payload, &sd); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad challenge"})
		return nil, false
	}
	return &sd, true
}

func (s *Server) handleVerifyWebAuthnBegin(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.localSession(w, r)
	if !ok {
		return
	}
	if sess.Level != auth.LevelMFA {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "no verification pending"})
		return
	}
	u, err := s.accounts.Store().GetUser(sess.UserID)
	if err != nil || u.WebAuthnCount == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no security key enrolled"})
		return
	}
	opts, sd, err := s.webauthn.BeginLogin(r, s.accounts.Store(), u)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := s.setWACookie(w, r, waCookieLogin, sd); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "session error"})
		return
	}
	writeJSON(w, http.StatusOK, opts)
}

func (s *Server) handleVerifyWebAuthnFinish(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.localSession(w, r)
	if !ok {
		return
	}
	if sess.Level != auth.LevelMFA {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "no verification pending"})
		return
	}
	sd, ok := s.readWACookie(w, r, waCookieLogin)
	if !ok {
		return
	}
	u, err := s.accounts.Store().GetUser(sess.UserID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "storage error"})
		return
	}
	if err := s.webauthn.FinishLogin(r, s.accounts.Store(), u, sd); err != nil {
		s.audit(r, "mfa.webauthn.verify_failed", "username", sess.User)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "verification failed"})
		return
	}
	if err := s.upgradeAfterMFA(w, r, sess); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "session error"})
		return
	}
	if r.URL.Query().Get("remember") == "true" {
		s.setTrustCookie(w, r, sess.UserID)
	}
	s.audit(r, "mfa.webauthn.verified", "username", sess.User)
	writeJSON(w, http.StatusOK, map[string]string{"level": auth.LevelFull})
}

// ---- enrolment (LevelEnroll during forced setup, or LevelFull in profile) ----

// canEnrol admits local sessions that are either forced-enrolling or fully
// authenticated (managing MFA from their profile).
func (s *Server) canEnrol(w http.ResponseWriter, r *http.Request) (*auth.Session, bool) {
	sess, ok := s.localSession(w, r)
	if !ok {
		return nil, false
	}
	if sess.Level != auth.LevelEnroll && sess.Level != auth.LevelFull {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return nil, false
	}
	return sess, true
}

func (s *Server) handleTOTPBegin(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.canEnrol(w, r)
	if !ok {
		return
	}
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "generation failed"})
		return
	}
	if err := s.accounts.Store().SetPendingTOTP(sess.UserID, secret); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "storage error"})
		return
	}
	uri := auth.TOTPURI(secret, sess.User, "Lightngx")
	writeJSON(w, http.StatusOK, map[string]string{"secret": secret, "uri": uri})
}

func (s *Server) handleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.canEnrol(w, r)
	if !ok {
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if !readJSON(w, r, &req, 1024) {
		return
	}
	secret, confirmed, err := s.accounts.Store().TOTPSecret(sess.UserID)
	if err != nil || secret == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "start enrolment first"})
		return
	}
	if confirmed {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "already enrolled"})
		return
	}
	matched, ok := auth.VerifyTOTP(secret, req.Code, 0)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid code"})
		return
	}
	if err := s.accounts.Store().ConfirmTOTP(sess.UserID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "storage error"})
		return
	}
	_ = s.accounts.Store().SetTOTPLastUsed(sess.UserID, matched)
	s.audit(r, "mfa.totp.enrolled", "username", sess.User)
	s.finishEnrol(w, r, sess)
}

func (s *Server) handleRegisterWebAuthnBegin(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.canEnrol(w, r)
	if !ok {
		return
	}
	u, err := s.accounts.Store().GetUser(sess.UserID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "storage error"})
		return
	}
	opts, sd, err := s.webauthn.BeginRegister(r, s.accounts.Store(), u)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := s.setWACookie(w, r, waCookieReg, sd); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "session error"})
		return
	}
	writeJSON(w, http.StatusOK, opts)
}

func (s *Server) handleRegisterWebAuthnFinish(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.canEnrol(w, r)
	if !ok {
		return
	}
	name := r.URL.Query().Get("name")
	sd, ok := s.readWACookie(w, r, waCookieReg)
	if !ok {
		return
	}
	u, err := s.accounts.Store().GetUser(sess.UserID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "storage error"})
		return
	}
	if err := s.webauthn.FinishRegister(r, s.accounts.Store(), u, sd, name); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "registration failed: " + err.Error()})
		return
	}
	s.audit(r, "mfa.webauthn.enrolled", "username", sess.User)
	s.finishEnrol(w, r, sess)
}

// finishEnrol upgrades a forced-enrolment session to full; a profile
// (already-full) session is left as-is.
func (s *Server) finishEnrol(w http.ResponseWriter, r *http.Request, sess *auth.Session) {
	if sess.Level == auth.LevelEnroll {
		if err := s.upgradeAfterMFA(w, r, sess); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "session error"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "enrolled", "level": auth.LevelFull})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "enrolled"})
}

// ---- profile management (LevelFull) ----

func (s *Server) handleListCredentials(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r.Context())
	creds, err := s.accounts.Store().Credentials(sess.UserID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "storage error"})
		return
	}
	out := make([]store.Credential, 0, len(creds))
	out = append(out, creds...)
	writeJSON(w, http.StatusOK, map[string]any{"credentials": out})
}

func (s *Server) handleDeleteCredential(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r.Context())
	id := r.URL.Query().Get("id")
	if err := s.accounts.Store().DeleteCredential(sess.UserID, id); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	s.audit(r, "mfa.webauthn.removed", "username", sess.User)
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

func (s *Server) handleDeleteTOTP(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r.Context())
	if err := s.accounts.Store().ClearTOTP(sess.UserID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "storage error"})
		return
	}
	s.audit(r, "mfa.totp.removed", "username", sess.User)
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r.Context())
	if sess.Method != "local" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not a local account"})
		return
	}
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !readJSON(w, r, &req, 4096) {
		return
	}
	_, hash, err := s.accounts.Store().PasswordHash(sess.User)
	if err != nil || !auth.CheckPassword(hash, req.Current) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "current password is incorrect"})
		return
	}
	if err := accounts.ValidatePassword(req.New); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	newHash, err := auth.HashPassword(req.New)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "hash error"})
		return
	}
	if err := s.accounts.Store().SetPassword(sess.UserID, newHash); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "storage error"})
		return
	}
	// Changing the password logs out every other device and drops remembered devices.
	_ = s.accounts.Store().DeleteUserSessions(sess.UserID, sess.Sid)
	_ = s.accounts.Store().ClearTrustedDevices(sess.UserID)
	s.audit(r, "account.password_changed", "username", sess.User)
	writeJSON(w, http.StatusOK, map[string]string{"status": "changed"})
}
