package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const oidcStateCookie = "ln_oidc_state"

// OIDC handles the authorization-code flow with PKCE against a single
// configured identity provider.
type OIDC struct {
	provider    *oidc.Provider
	verifier    *oidc.IDTokenVerifier
	oauth       oauth2.Config
	sessions    *Sessions
	allowGroups []string
	groupsClaim string
	adminGroups []string
}

type OIDCOptions struct {
	Issuer        string
	ClientID      string
	ClientSecret  string
	RedirectURL   string
	Scopes        []string
	AllowedGroups []string
	GroupsClaim   string
	AdminGroups   []string
	// Manual endpoints. When AuthURL is set the provider is built from
	// these instead of fetching /.well-known/openid-configuration, for
	// providers whose discovery is absent or wrong.
	AuthURL     string
	TokenURL    string
	JWKSURL     string
	UserInfoURL string
}

func NewOIDC(ctx context.Context, opts OIDCOptions, sessions *Sessions) (*OIDC, error) {
	var provider *oidc.Provider
	if opts.AuthURL != "" {
		provider = (&oidc.ProviderConfig{
			IssuerURL:   opts.Issuer,
			AuthURL:     opts.AuthURL,
			TokenURL:    opts.TokenURL,
			JWKSURL:     opts.JWKSURL,
			UserInfoURL: opts.UserInfoURL,
		}).NewProvider(ctx)
	} else {
		p, err := oidc.NewProvider(ctx, opts.Issuer)
		if err != nil {
			return nil, fmt.Errorf("oidc discovery: %w", err)
		}
		provider = p
	}
	return &OIDC{
		provider: provider,
		verifier: provider.Verifier(&oidc.Config{ClientID: opts.ClientID}),
		oauth: oauth2.Config{
			ClientID:     opts.ClientID,
			ClientSecret: opts.ClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  opts.RedirectURL,
			Scopes:       opts.Scopes,
		},
		sessions:    sessions,
		allowGroups: opts.AllowedGroups,
		groupsClaim: opts.GroupsClaim,
		adminGroups: opts.AdminGroups,
	}, nil
}

type oidcState struct {
	State    string    `json:"s"`
	Nonce    string    `json:"n"`
	Verifier string    `json:"v"`
	Expires  time.Time `json:"e"`
}

// Begin redirects the client to the identity provider.
func (o *OIDC) Begin(w http.ResponseWriter, r *http.Request) {
	st := oidcState{
		State:    randToken(),
		Nonce:    randToken(),
		Verifier: oauth2.GenerateVerifier(),
		Expires:  time.Now().Add(10 * time.Minute),
	}
	payload, _ := json.Marshal(st)
	http.SetCookie(w, &http.Cookie{
		Name:     oidcStateCookie,
		Value:    o.sessions.sign(payload),
		Path:     "/api/auth/oidc",
		MaxAge:   600,
		HttpOnly: true,
		Secure:   o.sessions.Secure(r),
		SameSite: http.SameSiteLaxMode,
	})
	url := o.oauth.AuthCodeURL(st.State,
		oauth2.S256ChallengeOption(st.Verifier),
		oidc.Nonce(st.Nonce))
	http.Redirect(w, r, url, http.StatusFound)
}

// Callback exchanges the authorization code, verifies the ID token and
// authorization rules, and returns the resulting username plus whether
// the identity maps to the admin role.
func (o *OIDC) Callback(w http.ResponseWriter, r *http.Request) (string, bool, error) {
	cookie, err := r.Cookie(oidcStateCookie)
	if err != nil {
		return "", false, errors.New("missing state cookie")
	}
	http.SetCookie(w, &http.Cookie{
		Name: oidcStateCookie, Value: "", Path: "/api/auth/oidc", MaxAge: -1,
		HttpOnly: true, Secure: o.sessions.Secure(r), SameSite: http.SameSiteLaxMode,
	})
	payload, err := o.sessions.verify(cookie.Value)
	if err != nil {
		return "", false, errors.New("bad state cookie")
	}
	var st oidcState
	if err := json.Unmarshal(payload, &st); err != nil || time.Now().After(st.Expires) {
		return "", false, errors.New("expired state cookie")
	}
	if r.URL.Query().Get("state") != st.State {
		return "", false, errors.New("state mismatch")
	}
	if errMsg := r.URL.Query().Get("error"); errMsg != "" {
		return "", false, fmt.Errorf("provider error: %s", errMsg)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	token, err := o.oauth.Exchange(ctx, r.URL.Query().Get("code"), oauth2.VerifierOption(st.Verifier))
	if err != nil {
		return "", false, fmt.Errorf("code exchange: %w", err)
	}
	rawID, ok := token.Extra("id_token").(string)
	if !ok {
		return "", false, errors.New("no id_token in response")
	}
	idToken, err := o.verifier.Verify(ctx, rawID)
	if err != nil {
		return "", false, fmt.Errorf("id token verify: %w", err)
	}
	if idToken.Nonce != st.Nonce {
		return "", false, errors.New("nonce mismatch")
	}

	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return "", false, err
	}
	email, _ := claims["email"].(string)
	username, _ := claims["preferred_username"].(string)
	if username == "" {
		username = email
	}
	if username == "" {
		username = idToken.Subject
	}

	if err := o.authorize(claims); err != nil {
		return "", false, err
	}
	return username, o.isAdmin(claims), nil
}

// groups extracts the string values of the configured groups claim.
func (o *OIDC) groups(claims map[string]any) []string {
	raw, ok := claims[o.groupsClaim].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, g := range raw {
		if gs, ok := g.(string); ok {
			out = append(out, gs)
		}
	}
	return out
}

// isAdmin reports whether the identity maps to the admin role via the
// LN_OIDC_ADMIN_GROUPS mapping.
func (o *OIDC) isAdmin(claims map[string]any) bool {
	for _, g := range o.groups(claims) {
		if slices.Contains(o.adminGroups, g) {
			return true
		}
	}
	return false
}

func (o *OIDC) authorize(claims map[string]any) error {
	if len(o.allowGroups) == 0 {
		return nil
	}
	for _, g := range o.groups(claims) {
		if slices.Contains(o.allowGroups, g) {
			return nil
		}
	}
	return errors.New("user not authorized by LN_OIDC_ALLOWED_GROUPS")
}

func randToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
