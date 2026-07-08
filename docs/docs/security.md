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
- Config mutations are serialized server-side, so concurrent saves never test
  or roll back against each other's half-applied changes. Saving a file that
  changed since you opened it returns a conflict the editor surfaces, rather
  than silently overwriting the other edit; your buffer is kept either way.
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

## Exposing it to the internet

With two-factor enabled, Lightngx should be safe to face the internet. That
said, a personal opinion from the maintainer: I would not put something this
sensitive (a tool that edits and reloads your nginx) directly on the public
internet, and if I did, I would add another authentication layer in front of
it. A gate at the nginx level means an attacker has to get through your
identity provider before they ever reach the login page.

The [hardened setup](./hardened.md) shows exactly that: an OIDC gate (with a
TOTP fallback for when your IdP is down) or a standalone TOTP gate, dropped in
front of the UI with the lua runtime the `:full` image already ships. The
ready-to-run files are in
[`example/hardened/`](https://github.com/buco7854/lightngx/tree/main/example/hardened).
