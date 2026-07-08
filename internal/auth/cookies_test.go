package auth

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewSecureFunc(t *testing.T) {
	_, lan, _ := net.ParseCIDR("10.0.0.0/8")
	trusted := []*net.IPNet{lan}

	newReq := func(remote, xfProto string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		if xfProto != "" {
			r.Header.Set("X-Forwarded-Proto", xfProto)
		}
		return r
	}

	cases := []struct {
		name   string
		mode   string
		remote string
		proto  string
		want   bool
	}{
		{"always forces on", "always", "203.0.113.9:1", "", true},
		{"never forces off", "never", "127.0.0.1:1", "https", false},
		{"auto loopback https", "auto", "127.0.0.1:1", "https", true},
		{"auto loopback http", "auto", "127.0.0.1:1", "http", false},
		{"auto trusted proxy https", "auto", "10.4.5.6:1", "https", true},
		{"auto untrusted peer ignores header", "auto", "203.0.113.9:1", "https", false},
		{"auto no header no tls", "auto", "127.0.0.1:1", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NewSecureFunc(c.mode, trusted)(newReq(c.remote, c.proto)); got != c.want {
				t.Errorf("got %v want %v", got, c.want)
			}
		})
	}
}
