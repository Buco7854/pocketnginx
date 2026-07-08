# Security

Lightngx is meant to be exposed publicly behind nginx itself, and it is
hardened for that.

- State-changing requests require a same-origin `Origin` or `Sec-Fetch-Site`
  header, which blocks cross-site request forgery. Sessions are HMAC-signed,
  HttpOnly cookies. Login and MFA are rate limited per IP, at five failures per
  five minutes. Sensitive actions are written to an audit log with the user and
  IP.
- Security headers are strict: a Content-Security-Policy of `default-src
  'self'`, `frame-ancestors 'none'`, nosniff, and a no-referrer policy.
- The editor is confined to the config directory. Both lexical path traversal
  and symlink escapes are blocked. Log access is limited to the configured
  paths, and to regular files only.
- Passwords use bcrypt at cost 12. A login that still owes a second factor gets
  a partial session that unlocks only the MFA endpoints. TOTP secrets are
  encrypted with AES-GCM at rest. WebAuthn uses the audited `go-webauthn`
  library with sign-counter checks. The last admin cannot be demoted or
  deleted.
- OIDC uses PKCE together with `state` and `nonce`.

## Recommended front proxy

Terminate TLS at the proxy with HTTP/2, and forward to
`http://127.0.0.1:9000`. Keep `LN_SECURE_COOKIES=true` and set
`LN_TRUSTED_PROXIES` to the proxy address so forwarded client IPs are trusted.

The live log stream is server-sent events, so the proxy must not buffer it. Set
`proxy_buffering off;`. The app also sends `X-Accel-Buffering: no` as a second
line of defense.
