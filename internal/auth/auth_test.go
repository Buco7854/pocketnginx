package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// insecure is a SecureFunc that never sets the Secure flag, for tests.
func insecure(*http.Request) bool { return false }

func TestSessionRoundTrip(t *testing.T) {
	s := NewSessions([]byte("0123456789abcdef0123456789abcdef"), time.Hour, insecure)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if err := s.Issue(rec, req, Session{User: "alice", Method: "local", Level: LevelFull}); err != nil {
		t.Fatal(err)
	}
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	sess, err := s.FromRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if sess.User != "alice" || sess.Method != "local" {
		t.Errorf("got %+v", sess)
	}
}

func TestSessionTampered(t *testing.T) {
	s := NewSessions([]byte("0123456789abcdef0123456789abcdef"), time.Hour, insecure)
	rec := httptest.NewRecorder()
	_ = s.Issue(rec, httptest.NewRequest(http.MethodGet, "/", nil), Session{User: "alice", Method: "local", Level: LevelFull})
	c := rec.Result().Cookies()[0]

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value[:len(c.Value)-2] + "xx"})
	if _, err := s.FromRequest(req); err == nil {
		t.Error("tampered token accepted")
	}

	other := NewSessions([]byte("ffffffffffffffffffffffffffffffff"), time.Hour, insecure)
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.AddCookie(c)
	if _, err := other.FromRequest(req2); err == nil {
		t.Error("token signed with another key accepted")
	}
}

func TestSessionExpiry(t *testing.T) {
	s := NewSessions([]byte("0123456789abcdef0123456789abcdef"), -time.Minute, insecure)
	rec := httptest.NewRecorder()
	_ = s.Issue(rec, httptest.NewRequest(http.MethodGet, "/", nil), Session{User: "alice", Method: "local", Level: LevelFull})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	if _, err := s.FromRequest(req); err == nil {
		t.Error("expired session accepted")
	}
}

func TestCheckPassword(t *testing.T) {
	hash, err := HashPassword("hunter2secret")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(hash, "hunter2secret") {
		t.Error("valid password rejected")
	}
	if CheckPassword(hash, "wrong") {
		t.Error("wrong password accepted")
	}
}

func TestRateLimiter(t *testing.T) {
	rl := NewRateLimiter(3, time.Minute)
	ip := "203.0.113.7"
	for i := 0; i < 3; i++ {
		if !rl.Allow(ip) {
			t.Fatalf("attempt %d should be allowed", i)
		}
		rl.Fail(ip)
	}
	if rl.Allow(ip) {
		t.Error("4th attempt should be blocked")
	}
	if !rl.Allow("198.51.100.1") {
		t.Error("other IP should be unaffected")
	}
	rl.Reset(ip)
	if !rl.Allow(ip) {
		t.Error("reset should unblock")
	}
}

func TestTOTPRoundTrip(t *testing.T) {
	secret, err := NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	code, ok := hotp(secret, uint64(time.Now().Unix()/totpPeriod))
	if !ok {
		t.Fatal("hotp failed")
	}
	if _, ok := VerifyTOTP(secret, code, 0); !ok {
		t.Fatal("valid code rejected")
	}
	if _, ok := VerifyTOTP(secret, "000000", 0); ok && code != "000000" {
		t.Fatal("wrong code accepted")
	}
	matched, _ := VerifyTOTP(secret, code, 0)
	if _, ok := VerifyTOTP(secret, code, matched); ok {
		t.Fatal("replay of consumed code accepted")
	}
	if len(code) != 6 {
		t.Fatalf("code len = %d", len(code))
	}
}
