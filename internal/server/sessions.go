package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/Buco7854/lightngx/internal/store"
)

const (
	trustCookie = "ln_trust"
	trustTTL    = 30 * 24 * time.Hour
)

// parseUA extracts a coarse browser and OS name from a User-Agent for
// display. It is best-effort, not a security control.
func parseUA(ua string) (browser, os string) {
	browser, os = "Unknown", "Unknown"
	switch {
	case strings.Contains(ua, "Edg"):
		browser = "Edge"
	case strings.Contains(ua, "OPR"), strings.Contains(ua, "Opera"):
		browser = "Opera"
	case strings.Contains(ua, "Firefox"):
		browser = "Firefox"
	case strings.Contains(ua, "Chrome"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari"):
		browser = "Safari"
	case strings.Contains(ua, "curl"):
		browser = "curl"
	}
	switch {
	case strings.Contains(ua, "Windows"):
		os = "Windows"
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"):
		os = "iOS"
	case strings.Contains(ua, "Mac OS X"):
		os = "macOS"
	case strings.Contains(ua, "Android"):
		os = "Android"
	case strings.Contains(ua, "Linux"):
		os = "Linux"
	}
	return browser, os
}

type sessionView struct {
	store.SessionRecord
	Browser string `json:"browser"`
	OS      string `json:"os"`
	Current bool   `json:"current"`
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r.Context())
	recs, err := s.accounts.Store().ListSessions(sess.UserID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "storage error"})
		return
	}
	out := make([]sessionView, 0, len(recs))
	for _, rec := range recs {
		b, o := parseUA(rec.UserAgent)
		out = append(out, sessionView{SessionRecord: rec, Browser: b, OS: o, Current: rec.Sid == sess.Sid})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

func (s *Server) handleRevokeSession(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r.Context())
	sid := r.PathValue("sid")
	if err := s.accounts.Store().DeleteSession(sid, sess.UserID); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	s.audit(r, "session.revoked", "username", sess.User)
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// setTrustCookie remembers the current device so future logins by this user
// can skip MFA until the cookie expires.
func (s *Server) setTrustCookie(w http.ResponseWriter, r *http.Request, userID int64) {
	val, err := s.accounts.Store().AddTrustedDevice(userID, r.UserAgent(), trustTTL)
	if err != nil {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: trustCookie, Value: s.sessions.Sign([]byte(val)), Path: "/api/auth",
		MaxAge: int(trustTTL.Seconds()), HttpOnly: true, Secure: s.sessions.Secure(r), SameSite: http.SameSiteLaxMode,
	})
}

// trustedDevice reports whether the request carries a valid trusted-device
// cookie for userID, letting login skip the second factor.
func (s *Server) trustedDevice(r *http.Request, userID int64) bool {
	c, err := r.Cookie(trustCookie)
	if err != nil {
		return false
	}
	payload, err := s.sessions.Unsign(c.Value)
	if err != nil {
		return false
	}
	return s.accounts.Store().TrustedDevice(userID, string(payload))
}
