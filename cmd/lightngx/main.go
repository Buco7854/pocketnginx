// Command lightngx is a lightweight web UI for managing nginx:
// config editing with syntax-check-guarded saves, reload/restart,
// and log viewing. Single binary, frontend embedded.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/Buco7854/lightngx/internal/accounts"
	"github.com/Buco7854/lightngx/internal/auth"
	"github.com/Buco7854/lightngx/internal/confdir"
	"github.com/Buco7854/lightngx/internal/config"
	"github.com/Buco7854/lightngx/internal/fsown"
	"github.com/Buco7854/lightngx/internal/logs"
	"github.com/Buco7854/lightngx/internal/nginxctl"
	"github.com/Buco7854/lightngx/internal/server"
	"github.com/Buco7854/lightngx/internal/sites"
	"github.com/Buco7854/lightngx/internal/store"
	"github.com/Buco7854/lightngx/internal/webauthnx"
	"github.com/Buco7854/lightngx/web"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "hash":
			runHash()
			return
		case "health":
			runHealth()
			return
		case "version", "-v", "--version":
			fmt.Println("lightngx", version)
			return
		}
	}
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func runHash() {
	var pass string
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, "Password: ")
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		pass = string(b)
	} else {
		var b [512]byte
		n, _ := os.Stdin.Read(b[:])
		pass = strings.TrimRight(string(b[:n]), "\r\n")
	}
	if len(pass) < 8 {
		fmt.Fprintln(os.Stderr, "password must be at least 8 characters")
		os.Exit(1)
	}
	h, err := auth.HashPassword(pass)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(h)
}

// runHealth probes the local HTTP endpoint; used as the Docker
// HEALTHCHECK since the nginx base image ships no curl/wget.
func runHealth() {
	listen := os.Getenv("LN_LISTEN")
	if listen == "" {
		listen = ":9000"
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bad LN_LISTEN:", err)
		os.Exit(1)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/api/auth/status")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "status", resp.StatusCode)
		os.Exit(1)
	}
	fmt.Println("ok")
}

func run() error {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	secret, err := auth.LoadOrCreateSecret(cfg.SessionSecret, cfg.DataDir)
	if err != nil {
		return err
	}
	sessions := auth.NewSessions(secret, cfg.SessionTTL,
		auth.NewSecureFunc(cfg.CookieSecure, cfg.TrustedProxies))

	encKey, err := auth.LoadOrCreateDataKey(cfg.DataDir)
	if err != nil {
		return err
	}

	if len(cfg.TrustedProxies) == 0 {
		slog.Warn("LN_TRUSTED_PROXIES is empty: behind a reverse proxy, rate limiting and audit logs key on the proxy address; set it to your proxy's CIDR")
	}
	if cfg.WebAuthnRPID == "" {
		slog.Warn("LN_WEBAUTHN_RPID/ORIGINS unset: WebAuthn binds to the request Host; set them for a stable identity behind a proxy")
	}

	nginx := nginxctl.New(cfg.NginxBin, cfg.NginxConf, cfg.NginxPidFile, cfg.Supervise, cfg.Logrotate)
	if cfg.Supervise {
		if err := nginx.StartSupervised(); err != nil {
			// Keep the UI up even if nginx cannot start (broken config):
			// the whole point is being able to fix it from here.
			slog.Error("nginx failed to start, UI stays available", "error", err)
		}
	}

	conf, err := confdir.New(cfg.NginxConfDir, cfg.MaxEditSize)
	if err != nil {
		return err
	}
	if cfg.FixConfigPerms {
		if u, g, name, ok := resolveNginxUser(cfg); ok {
			fsown.Configure(u, g)
			slog.Info("UI-created config files will be owned by the nginx worker user", "user", name)
		}
	}
	logStore := logs.New(cfg.LogPaths)

	userStore, err := store.Open(cfg.DBPath, encKey)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer userStore.Close()
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for range t.C {
			_ = userStore.DeleteExpiredSessions()
		}
	}()
	acct, err := accounts.New(userStore, cfg)
	if err != nil {
		return fmt.Errorf("accounts: %w", err)
	}
	webAuthn := webauthnx.New(cfg.WebAuthnRPID, cfg.WebAuthnOrigins)

	var siteMgr, streamMgr *sites.Manager
	if cfg.SitesEnabled {
		siteMgr = sites.New(cfg.SitesAvailableDir, cfg.SitesEnabledDir,
			filepath.Join(cfg.NginxConfDir, ".lightngx"), cfg.MaintenancePage)
	}
	if cfg.StreamsEnabled {
		streamMgr = sites.New(cfg.StreamsAvailableDir, cfg.StreamsEnabledDir,
			filepath.Join(cfg.NginxConfDir, ".lightngx"), "").AsStream()
	}

	var oidcClient *auth.OIDC
	if cfg.OIDCEnabled() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		oidcClient, err = auth.NewOIDC(ctx, auth.OIDCOptions{
			Issuer:        cfg.OIDCIssuer,
			ClientID:      cfg.OIDCClientID,
			ClientSecret:  cfg.OIDCClientSecret,
			RedirectURL:   cfg.OIDCRedirectURL,
			Scopes:        cfg.OIDCScopes,
			AllowedGroups: cfg.OIDCAllowedGroups,
			GroupsClaim:   cfg.OIDCGroupsClaim,
			AdminGroups:   cfg.OIDCAdminGroups,
			AuthURL:       cfg.OIDCAuthURL,
			TokenURL:      cfg.OIDCTokenURL,
			JWKSURL:       cfg.OIDCJWKSURL,
			UserInfoURL:   cfg.OIDCUserInfoURL,
		}, sessions)
		cancel()
		if err != nil {
			return err
		}
	}

	srv := server.New(cfg, sessions, oidcClient, nginx, conf, logStore, siteMgr, streamMgr, acct, webAuthn, web.Dist())
	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("lightngx listening", "addr", cfg.Listen, "version", version)
		errCh <- httpSrv.ListenAndServe()
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-sigCh:
		slog.Info("shutting down", "signal", sig.String())
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			nginx.Shutdown()
			return err
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
	nginx.Shutdown()
	return nil
}

// resolveNginxUser finds the uid/gid of the nginx worker user: cfg.NginxUser
// when set, otherwise the `user` directive in nginx.conf, defaulting to
// "nginx". Returns ok=false when the user cannot be looked up.
func resolveNginxUser(cfg *config.Config) (uid, gid int, name string, ok bool) {
	name = cfg.NginxUser
	if name == "" {
		name = nginxUserDirective(cfg.NginxConf)
	}
	if name == "" {
		name = "nginx"
	}
	u, err := user.Lookup(name)
	if err != nil {
		return 0, 0, name, false
	}
	uid, err = strconv.Atoi(u.Uid)
	if err != nil {
		return 0, 0, name, false
	}
	gid, err = strconv.Atoi(u.Gid)
	if err != nil {
		return 0, 0, name, false
	}
	return uid, gid, name, true
}

// nginxUserDirective returns the username from the top-level `user` directive
// in nginx.conf, or "" if absent.
func nginxUserDirective(confPath string) string {
	b, err := os.ReadFile(confPath)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "user" {
			return strings.TrimSuffix(f[1], ";")
		}
	}
	return ""
}
