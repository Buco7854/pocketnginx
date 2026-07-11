// Package config loads all runtime settings from LN_* environment variables.
package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds every runtime setting. All values come from LN_* environment
// variables with defaults matching a standard nginx Docker deployment.
type Config struct {
	Listen  string
	DataDir string

	NginxBin     string
	NginxConf    string
	NginxConfDir string
	NginxPidFile string
	// Supervise: lightngx starts and manages nginx as a child process.
	// When false, nginx runs under an external supervisor (s6, systemd...)
	// and restart is delegated to it via SIGQUIT on the pidfile pid.
	Supervise bool
	// Logrotate: when supervising, run logrotate on a timer (there is no
	// cron in the image). On by default; a no-op outside the image where
	// logrotate or its config are absent.
	Logrotate bool

	// DefaultReloadOnSave is the default for whether a config change reloads
	// nginx after it passes nginx -t. The editor's Save button can override
	// it per save.
	DefaultReloadOnSave bool

	// FixConfigPerms chowns files the UI creates to NginxUser (the nginx
	// worker user, auto-detected from nginx.conf when NginxUser is empty).
	FixConfigPerms bool
	NginxUser      string

	LogPaths []string

	// Sites management (Debian sites-available/sites-enabled convention)
	// and the per-site maintenance page. Both on by default. Streams use
	// the same convention (streams-available/streams-enabled) for the
	// nginx stream{} context; no maintenance mode there (no HTTP).
	SitesEnabled        bool
	MaintenanceEnabled  bool
	SitesAvailableDir   string
	SitesEnabledDir     string
	MaintenancePage     string
	StreamsEnabled      bool
	StreamsAvailableDir string
	StreamsEnabledDir   string

	// Seed admin: creates a first admin row in the user DB on startup if
	// that username does not exist yet. Optional: the web setup page
	// handles first-run when unset.
	AdminUser         string
	AdminPasswordHash string

	// DBPath is the SQLite file for local users and settings.
	DBPath string

	// MFA policy. When LN_MFA_REQUIRED_ROLES is set it pins the policy
	// (admins can't change it in the UI); otherwise the first admin
	// decides it in-app. Roles are a subset of {admin, user}.
	MFARolesPinned   bool
	MFARequiredRoles []string

	OIDCIssuer        string
	OIDCClientID      string
	OIDCClientSecret  string
	OIDCRedirectURL   string
	OIDCScopes        []string
	OIDCAllowedGroups []string
	OIDCGroupsClaim   string
	// OIDC identities in these groups are granted the admin role; all
	// other OIDC logins are role "user". OIDC bypasses local MFA.
	OIDCAdminGroups []string
	// Provider name shown on the login button ("Sign in with <label>");
	// empty falls back to the generic SSO wording.
	OIDCLabel string
	// Manual endpoint overrides, used instead of OIDC discovery when a
	// provider has no /.well-known/openid-configuration or serves a wrong
	// one. Auth, token and JWKS must be set together; userinfo is optional.
	OIDCAuthURL     string
	OIDCTokenURL    string
	OIDCJWKSURL     string
	OIDCUserInfoURL string

	// WebAuthn relying-party identity. When empty they are derived from
	// the request host/origin at enrollment time (works behind a proxy
	// that preserves Host + X-Forwarded-Proto).
	WebAuthnRPID    string
	WebAuthnOrigins []string

	SessionSecret string
	SessionTTL    time.Duration
	// CookieSecure is the Secure-flag policy for auth cookies: "auto"
	// (mirror the request scheme), "always", or "never".
	CookieSecure   string
	TrustedProxies []*net.IPNet

	MaxEditSize int64
}

func Load() (*Config, error) {
	c := &Config{
		Listen:       env("LN_LISTEN", ":9000"),
		DataDir:      env("LN_DATA_DIR", "/var/lib/lightngx"),
		NginxBin:     env("LN_NGINX_BIN", "nginx"),
		NginxConf:    env("LN_NGINX_CONF", "/etc/nginx/nginx.conf"),
		NginxConfDir: env("LN_NGINX_CONF_DIR", "/etc/nginx"),
		NginxPidFile: env("LN_NGINX_PID", "/var/run/nginx.pid"),
		Supervise:    envBool("LN_SUPERVISE", false),
		Logrotate:    envBool("LN_LOGROTATE", true),

		DefaultReloadOnSave: envBool("LN_DEFAULT_RELOAD_ON_SAVE", true),
		FixConfigPerms:      envBool("LN_FIX_CONFIG_PERMS", true),
		NginxUser:           env("LN_NGINX_USER", ""),

		LogPaths: splitPaths(env("LN_LOG_PATHS", "/var/log/nginx")),

		SitesEnabled:        envBool("LN_SITES", true),
		MaintenanceEnabled:  envBool("LN_MAINTENANCE", true),
		SitesAvailableDir:   env("LN_SITES_AVAILABLE_DIR", ""),
		SitesEnabledDir:     env("LN_SITES_ENABLED_DIR", ""),
		MaintenancePage:     env("LN_MAINTENANCE_PAGE", ""),
		StreamsEnabled:      envBool("LN_STREAMS", true),
		StreamsAvailableDir: env("LN_STREAMS_AVAILABLE_DIR", ""),
		StreamsEnabledDir:   env("LN_STREAMS_ENABLED_DIR", ""),

		AdminUser:         env("LN_ADMIN_USER", ""),
		AdminPasswordHash: env("LN_ADMIN_PASSWORD_HASH", ""),

		DBPath: env("LN_DB_PATH", ""),

		OIDCIssuer:        env("LN_OIDC_ISSUER", ""),
		OIDCClientID:      env("LN_OIDC_CLIENT_ID", ""),
		OIDCClientSecret:  env("LN_OIDC_CLIENT_SECRET", ""),
		OIDCRedirectURL:   env("LN_OIDC_REDIRECT_URL", ""),
		OIDCScopes:        splitList(env("LN_OIDC_SCOPES", "openid,profile,email")),
		OIDCAllowedGroups: splitList(env("LN_OIDC_ALLOWED_GROUPS", "")),
		OIDCGroupsClaim:   env("LN_OIDC_GROUPS_CLAIM", "groups"),
		OIDCAdminGroups:   splitList(env("LN_OIDC_ADMIN_GROUPS", "")),
		OIDCLabel:         env("LN_OIDC_LABEL", ""),
		OIDCAuthURL:       env("LN_OIDC_AUTH_URL", ""),
		OIDCTokenURL:      env("LN_OIDC_TOKEN_URL", ""),
		OIDCJWKSURL:       env("LN_OIDC_JWKS_URL", ""),
		OIDCUserInfoURL:   env("LN_OIDC_USERINFO_URL", ""),

		WebAuthnRPID:    env("LN_WEBAUTHN_RPID", ""),
		WebAuthnOrigins: splitList(env("LN_WEBAUTHN_ORIGINS", "")),

		SessionSecret: env("LN_SESSION_SECRET", ""),
		CookieSecure:  cookieSecureMode(env("LN_SECURE_COOKIES", "auto")),

		MaxEditSize: envInt64("LN_MAX_EDIT_SIZE", 2<<20),
	}

	if raw, ok := os.LookupEnv("LN_MFA_REQUIRED_ROLES"); ok {
		c.MFARolesPinned = true
		for _, role := range splitList(raw) {
			if role != "admin" && role != "user" {
				return nil, fmt.Errorf("LN_MFA_REQUIRED_ROLES: unknown role %q (allowed: admin, user)", role)
			}
			c.MFARequiredRoles = append(c.MFARequiredRoles, role)
		}
	}

	ttl, err := time.ParseDuration(env("LN_SESSION_TTL", "12h"))
	if err != nil {
		return nil, fmt.Errorf("LN_SESSION_TTL: %w", err)
	}
	c.SessionTTL = ttl

	for _, cidr := range splitList(env("LN_TRUSTED_PROXIES", "")) {
		if !strings.Contains(cidr, "/") {
			if strings.Contains(cidr, ":") {
				cidr += "/128"
			} else {
				cidr += "/32"
			}
		}
		_, ipnet, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("LN_TRUSTED_PROXIES: %w", err)
		}
		c.TrustedProxies = append(c.TrustedProxies, ipnet)
	}

	if c.SitesAvailableDir == "" {
		c.SitesAvailableDir = c.NginxConfDir + "/sites-available"
	}
	if c.SitesEnabledDir == "" {
		c.SitesEnabledDir = c.NginxConfDir + "/sites-enabled"
	}
	if c.StreamsAvailableDir == "" {
		c.StreamsAvailableDir = c.NginxConfDir + "/streams-available"
	}
	if c.StreamsEnabledDir == "" {
		c.StreamsEnabledDir = c.NginxConfDir + "/streams-enabled"
	}
	if c.DBPath == "" {
		c.DBPath = c.DataDir + "/lightngx.db"
	}

	if c.AdminUser != "" && c.AdminPasswordHash == "" {
		return nil, fmt.Errorf("LN_ADMIN_USER is set but LN_ADMIN_PASSWORD_HASH is empty (generate one with: lightngx hash)")
	}
	if c.OIDCIssuer != "" && (c.OIDCClientID == "" || c.OIDCRedirectURL == "") {
		return nil, fmt.Errorf("OIDC requires LN_OIDC_CLIENT_ID and LN_OIDC_REDIRECT_URL")
	}
	if (c.OIDCAuthURL != "" || c.OIDCTokenURL != "" || c.OIDCJWKSURL != "" || c.OIDCUserInfoURL != "") &&
		(c.OIDCAuthURL == "" || c.OIDCTokenURL == "" || c.OIDCJWKSURL == "") {
		return nil, fmt.Errorf("OIDC endpoint overrides need LN_OIDC_AUTH_URL, LN_OIDC_TOKEN_URL and LN_OIDC_JWKS_URL set together")
	}

	return c, nil
}

// OIDCEnabled reports whether OIDC login is configured.
func (c *Config) OIDCEnabled() bool { return c.OIDCIssuer != "" }

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

// cookieSecureMode normalises LN_SECURE_COOKIES to "always", "never" or
// "auto". Booleans (true/false, 1/0) map to always/never for compatibility;
// anything else, including the default, is auto.
func cookieSecureMode(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "always":
		return "always"
	case "false", "0", "no", "never":
		return "never"
	default:
		return "auto"
	}
}

func envBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envInt64(key string, def int64) int64 {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// splitPaths accepts both comma- and colon-separated path lists.
func splitPaths(s string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ':' }) {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
