package auth

import (
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
)

// NewSecureFunc builds the per-request Secure-flag policy for auth cookies.
//
//	"always" / "never" force the flag.
//	"auto" (default) mirrors the request scheme: Secure over HTTPS, not over
//	plain HTTP. The scheme comes from X-Forwarded-Proto, trusted only from the
//	loopback peer (the bundled nginx proxies from 127.0.0.1) or a configured
//	trusted proxy; otherwise from the TLS state of the connection.
//
// Auto lets one instance serve HTTP on the LAN and HTTPS from a front proxy
// without an env change. Behind a separate proxy, set LN_TRUSTED_PROXIES (as
// you already do for real client IPs) so the forwarded scheme is honoured.
func NewSecureFunc(mode string, trusted []*net.IPNet) SecureFunc {
	switch mode {
	case "always":
		return func(*http.Request) bool { return true }
	case "never":
		return func(*http.Request) bool { return false }
	}
	var warnOnce sync.Once
	return func(r *http.Request) bool {
		if peerTrusted(r, trusted) {
			if xf := r.Header.Get("X-Forwarded-Proto"); xf != "" {
				return forwardedHTTPS(xf)
			}
		}
		// An untrusted peer presenting HTTPS is the separate-proxy case: we
		// can't believe the header, so the cookie stays non-Secure. Expose
		// :9000 directly only for local HTTP; front any other access with the
		// bundled nginx, or set LN_TRUSTED_PROXIES to the proxy.
		if r.TLS == nil && forwardedHTTPS(r.Header.Get("X-Forwarded-Proto")) {
			warnOnce.Do(func() {
				slog.Warn("auto cookies: HTTPS request via an untrusted proxy, cookie left non-Secure; put the UI behind the bundled nginx or set LN_TRUSTED_PROXIES (direct :9000 is for local HTTP only)")
			})
		}
		return r.TLS != nil
	}
}

// forwardedHTTPS reports whether an X-Forwarded-Proto value (possibly a
// comma-separated chain) names https in its first hop.
func forwardedHTTPS(xf string) bool {
	if xf == "" {
		return false
	}
	if i := strings.IndexByte(xf, ','); i >= 0 {
		xf = xf[:i]
	}
	return strings.EqualFold(strings.TrimSpace(xf), "https")
}

// peerTrusted reports whether the direct peer is loopback or a configured
// trusted proxy, i.e. whether its X-Forwarded-* headers may be believed.
func peerTrusted(r *http.Request, trusted []*net.IPNet) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	for _, cidr := range trusted {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}
