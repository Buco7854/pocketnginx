import CodeBlock from "@theme/CodeBlock";
import oidcSnippet from "!!raw-loader!@site/../example/hardened/snippets/oidc-gate.conf";
import totpSnippet from "!!raw-loader!@site/../example/hardened/snippets/totp-gate.conf";
import gateSetup from "!!raw-loader!@site/../example/hardened/conf.d/00-auth-gate.conf";
import gatedVhost from "!!raw-loader!@site/../example/hardened/conf.d/10-lightngx-gated.conf.example";
import gateResolver from "!!raw-loader!@site/../example/hardened/conf.d/01-gate-resolver.conf.example";
import hardenedCompose from "!!raw-loader!@site/../example/hardened/docker-compose.yml";
import hardenedEnv from "!!raw-loader!@site/../example/hardened/.env.example";
import crowdsecLocal from "!!raw-loader!@site/../example/hardened/crowdsec/conf/config.yaml.local";
import oidcLua from "!!raw-loader!@site/../example/hardened/nginx/lua/oidc_gate.lua";
import totpGateLua from "!!raw-loader!@site/../example/hardened/nginx/lua/totp_gate.lua";
import totpLua from "!!raw-loader!@site/../example/hardened/nginx/lua/totp.lua";

# Hardened setup

Two-factor makes Lightngx safe to expose, but as noted under
[Security](./security.md#exposing-it-to-the-internet), putting a tool that
edits and reloads nginx on the public internet is worth an extra layer. This
page adds an **authentication gate at the nginx level**, in front of the
Lightngx login, so an unauthenticated request never reaches the app.

It uses the lua runtime the [`:full` image](./images.md) already ships
(lua-nginx-module plus lua-resty-openidc); the light image does not have it.
The complete, ready-to-run files live in
[`example/hardened/`](https://github.com/buco7854/lightngx/tree/main/example/hardened).

## Two gates

Pick one per vhost. A gate is a single `rewrite_by_lua_block`, and nginx
allows only one per `server {}`.

| Gate | What it does | When to use |
| --- | --- | --- |
| **OIDC** | Authenticates at your existing IdP (Authelia, Authentik, Keycloak, Zitadel, …). Falls back to a TOTP code if the IdP is unreachable, so an IdP outage never locks you out. | You already run an identity provider |
| **TOTP** | A self-contained RFC 6238 code prompt with per-IP brute-force lockout. No external service. | You want a second factor with no IdP |

Both run in the rewrite phase, so they coexist with the CrowdSec bouncer, which
the hardened example also runs: the gate stops unauthenticated humans at the
door while CrowdSec bans bad actors at the edge.

## How the OIDC gate fails over

The OIDC gate is a thin wrapper around `lua-resty-openidc` with a circuit
breaker. An IdP call that fails (cold discovery, JWKS, or token exchange), or
a background health probe that misses, opens the circuit for a short TTL.
While it is open, requests route to the TOTP fallback instead of bouncing
users into a dead IdP. Once the IdP recovers the circuit closes on its own and
OIDC resumes. If you configure no fallback, an IdP outage returns
`503 Retry-After` instead.

Requests that authenticate get `X-Auth-Sub`, `X-Auth-User` and `X-Auth-Email`
set from the verified identity, forwarded to Lightngx.

## What the gate is made of

The gate is a small set of files, each with a fixed home in nginx:

| File (in `example/hardened/`) | Where it goes in nginx | What it is |
| --- | --- | --- |
| `nginx/lua/*.lua` | `/usr/local/share/lua/5.1/` (the lua require path) | the gate scripts |
| `conf.d/00-auth-gate.conf` | `/etc/nginx/conf.d/` | the gate's shared dictionaries (http context) |
| `snippets/*.conf` | `/etc/nginx/snippets/` | the gate you `include` in a vhost |
| `gates/oidc/`, `gates/totp/` | `/etc/nginx/gates/` | your secret key files (you create these) |
| `gates/whitelist_ips` (optional) | `/etc/nginx/gates/` | an IP allowlist that skips the gate |
| `conf.d/01-gate-resolver.conf` (no-CrowdSec only) | `/etc/nginx/conf.d/` | `resolver` + `lua_ssl_*`, which CrowdSec otherwise provides |

**Only the lua is a bind mount** (it is the one piece that lives outside
`/etc/nginx`, so the compose mounts it). Everything else you save into your nginx
config directory, which is already a single mount. Each file is shown in the
setup below.

## Set it up

You do not clone or copy anything from the repo. Every file you need is shown
below; save each one to the path in its title. The flow is: create the compose
and `.env`, start once so the container seeds `./nginx/conf` (which is
`/etc/nginx`), then save the gate files into that seeded directory and reload.

:::note Already run your own nginx?
Skip the compose and `.env` and the seeding start (steps 1 and 2), but you still
need the three gate `*.lua` shown in step 1: put them onto your nginx's lua
require path (`/usr/local/share/lua/5.1/`, a bind mount or your image). Save the
rest of the config files under your own `/etc/nginx` instead of `./nginx/conf`,
run the `:full` image (or your own image built from it, for the lua runtime), and
use your own `nginx -t && nginx -s reload`. If you do **not** run the CrowdSec
bouncer, also add `01-gate-resolver.conf` (shown under [Does it
conflict?](#does-it-conflict-with-the-config-lightngx-seeds)) so the gate has a
`resolver` and TLS trust; with CrowdSec, its seeded config already provides them
and nginx rejects duplicates.
:::

### 1. Create the compose, env, and gate scripts

Save this as `docker-compose.yml`. It is the full example plus the three gate
`*.lua` mounts, nothing else:

<CodeBlock language="yaml" title="docker-compose.yml">{hardenedCompose}</CodeBlock>

Save this as `.env` beside it and fill the secrets it marks required
(`CROWDSEC_BOUNCER_KEY`, `CROWDSEC_DB_PASSWORD`), and set `LN_SESSION_SECRET`:

<CodeBlock language="ini" title=".env">{hardenedEnv}</CodeBlock>

The compose above mounts three gate scripts from `./nginx/lua/` onto nginx's lua
require path (`/usr/local/share/lua/5.1/`), so they must exist **before** you
start, or Docker creates empty directories in their place. They are plain lua you
own and can edit. Create `nginx/lua/` and save all three there (download them
from
[`example/hardened/nginx/lua/`](https://github.com/buco7854/lightngx/tree/main/example/hardened/nginx/lua),
or copy them from here):

<details>
<summary>The three gate scripts (save under <code>nginx/lua/</code>)</summary>

<CodeBlock language="lua" title="nginx/lua/oidc_gate.lua">{oidcLua}</CodeBlock>

<CodeBlock language="lua" title="nginx/lua/totp_gate.lua">{totpGateLua}</CodeBlock>

<CodeBlock language="lua" title="nginx/lua/totp.lua">{totpLua}</CodeBlock>

</details>

CrowdSec runs on the Postgres service, but the image has no `DB_*` environment
variables — the DB connection has to come from a mounted `config.yaml.local`
(without it CrowdSec silently uses SQLite and ignores Postgres). Save this as
`crowdsec/conf/config.yaml.local`; it reads the credentials from your `.env`:

<CodeBlock language="yaml" title="crowdsec/conf/config.yaml.local">{crowdsecLocal}</CodeBlock>

### 2. Start once to seed the config

```sh
docker compose up -d
```

The first start seeds `./nginx/conf` (which is `/etc/nginx`). nginx runs without
the gate for now; you add it in the next steps and reload at the end.

### 3. Save the shared dictionaries

Save this as `nginx/conf/conf.d/00-auth-gate.conf`. It declares the gate's shared
memory and belongs in the http context, so it goes in `conf.d/`:

<CodeBlock language="nginx" title="nginx/conf/conf.d/00-auth-gate.conf">{gateSetup}</CodeBlock>

### 4. Save the gate snippet

For an **OIDC gate** (with the TOTP fallback), save this as
`nginx/conf/snippets/oidc-gate.conf`, then set `discovery` and `client_id` for
your IdP. The snippet's `redirect_uri` is just the path `/__oidc_callback`, which
lua-resty-openidc expands to `https://<your-host>/__oidc_callback` from the
request, so there is no domain to hardcode; register that URL as an allowed
redirect URI at your IdP:

<CodeBlock language="nginx" title="nginx/conf/snippets/oidc-gate.conf">{oidcSnippet}</CodeBlock>

For a **TOTP-only gate**, save this as `nginx/conf/snippets/totp-gate.conf`
instead. It needs no IdP and no `gates/oidc/` files:

<CodeBlock language="nginx" title="nginx/conf/snippets/totp-gate.conf">{totpSnippet}</CodeBlock>

### 5. Create the key files

The TOTP gate, and the OIDC gate's fallback, read two files from
`nginx/conf/gates/totp/`. These are generated, so run:

```sh
mkdir -p nginx/conf/gates/totp
head -c 20 /dev/urandom | base32 > nginx/conf/gates/totp/secret       # the shared TOTP secret
head -c 32 /dev/urandom          > nginx/conf/gates/totp/cookie_key   # signs the gate's session cookie
```

Enroll the secret in your authenticator app (Aegis, Google Authenticator,
1Password, …) with this URI, pasting the string from the `secret` file:

```
otpauth://totp/Lightngx?secret=<the base32 secret>&issuer=Lightngx
```

For the **OIDC gate**, also create its two keys in `nginx/conf/gates/oidc/` (skip
for a TOTP-only gate):

```sh
mkdir -p nginx/conf/gates/oidc
printf '%s' 'YOUR_OIDC_CLIENT_SECRET' > nginx/conf/gates/oidc/client_secret   # the secret your IdP issued
head -c 32 /dev/urandom > nginx/conf/gates/oidc/session_secret                # encrypts the OIDC session cookie
```

### 6. Let the worker read the keys

The key files must be owned by the user nginx runs its **workers** as (the `user`
directive in your `nginx.conf`). You do not have to chase that user down: on every
start the container **owns `/etc/nginx` as the worker user and locks the gate
dir** for you. So the simplest way to apply it after adding the keys is to restart
— which in the next step also loads the gate:

```sh
docker compose restart nginx
```

If you would rather apply it live (for an `nginx -s reload` instead of a
restart), do the same chown by hand from a container so the name resolves to the
right uid:

```sh
docker compose run --rm --no-deps --entrypoint sh nginx \
  -c 'chown -R nginx:nginx /etc/nginx && chmod -R go-rwx /etc/nginx/gates'
```

Swap `nginx:nginx` for `www-data:www-data` if that is your `user` directive
(`grep -E '^\s*user' nginx/conf/nginx.conf`). To turn the automatic ownership off
entirely, set `LN_FIX_CONFIG_PERMS=false`.

### 7. Gate your public vhost

Save this as `nginx/conf/conf.d/10-lightngx-gated.conf`, then set `server_name`
and the certificate paths. The line that switches the gate on is
`include /etc/nginx/snippets/oidc-gate.conf;` (or `totp-gate.conf`):

<CodeBlock language="nginx" title="nginx/conf/conf.d/10-lightngx-gated.conf">{gatedVhost}</CodeBlock>

This is the internet-facing vhost. Lightngx also seeds its own
`conf.d/lightngx.conf` on first start: a private-network-only proxy to the UI on
`:9001`, over plain HTTP, for local access. It coexists with this gated vhost
(different port, private IPs only); leave it for LAN use, or delete it if you
want the gate to be the only way in.

### 8. Restart to apply

Restarting re-runs the ownership step and loads the gate in one go:

```sh
docker compose exec nginx nginx -t   # optional: check the config first
docker compose restart nginx
# your own nginx: sudo nginx -t && sudo nginx -s reload (after chowning the keys)
```

Browse your domain: you now pass the IdP (or a TOTP code) before you ever reach
Lightngx's own login.

<details>
<summary>The shell steps in one block (the .conf files you save by hand)</summary>

```sh
# The .conf files come from the code blocks above (save each by hand). These are
# only the generated keys and the restart.

# keys
mkdir -p nginx/conf/gates/totp nginx/conf/gates/oidc
head -c 20 /dev/urandom | base32 > nginx/conf/gates/totp/secret
head -c 32 /dev/urandom          > nginx/conf/gates/totp/cookie_key
printf '%s' 'YOUR_OIDC_CLIENT_SECRET' > nginx/conf/gates/oidc/client_secret   # OIDC only
head -c 32 /dev/urandom > nginx/conf/gates/oidc/session_secret                # OIDC only

# restart: the entrypoint owns /etc/nginx as the worker user, then nginx loads the gate
docker compose exec nginx nginx -t && docker compose restart nginx
```

</details>

Keep `.env` and everything under `nginx/conf/gates` (the keys) out of any git
repo; they are secrets.

## Custom login and maintenance pages

Two HTML pages can be overridden, and by convention both live in
`nginx/conf/templates/` (which is `/etc/nginx/templates/` in the container).
Neither is required: leave the setting unset and the built-in page is used.

- **TOTP login page.** The TOTP gate ships a baked-in login page. To use your
  own, save it to `nginx/conf/templates/totp-login.html` and point the snippet at
  it: uncomment `login_template_file = "/etc/nginx/templates/totp-login.html"` in
  `snippets/totp-gate.conf` (or in `oidc-gate.conf`'s fallback block). The
  template must keep the `__ACTION__` placeholder; a missing, empty or malformed
  file silently falls back to the baked default, so a typo can never lock you
  out.
- **Maintenance page.** Lightngx serves a built-in 503 page while maintenance
  mode is on. To use your own, save it to `nginx/conf/templates/maintenance.html`
  and set `LN_MAINTENANCE_PAGE=/etc/nginx/templates/maintenance.html` in `.env`.

## Does it conflict with the config Lightngx seeds?

No, with one thing to know. The gate runs in the **rewrite** phase, so it sits
beside the CrowdSec bouncer's access phase rather than shadowing it; the seeded
LAN UI proxy listens on a different port; and the gate's shared-dict names are
unique. The catch is that CrowdSec's seeded config already sets
`lua_package_path` and `lua_ssl_*` (in `crowdsec_nginx.conf`) and `resolver`
(in `resolver.conf`), and nginx rejects a duplicate of any of them. So this
example never sets them: the lua lives on the default require path (no
`lua_package_path`), and `resolver` + `lua_ssl_*` come from CrowdSec. Running
the gate **without** CrowdSec is the one case where you add them back: save this
as `nginx/conf/conf.d/01-gate-resolver.conf`.

<CodeBlock language="nginx" title="nginx/conf/conf.d/01-gate-resolver.conf">{gateResolver}</CodeBlock>

## Security notes

- **Never publish the UI port (9000) directly** while a gate is in front. The
  `X-Auth-*` headers are only trustworthy because every request is forced
  through the gate; a second, ungated path lets anyone forge them.
- **Whitelist** trusted networks (a LAN, a static office IP, a Tailscale range)
  in `gates/whitelist_ips` to skip the gate for them.
- The gates return `401` on a genuine credential failure (so CrowdSec's
  brute-force scenarios can act) but `419` for a SPA that simply has no session
  yet, so ordinary polling never trips a ban.

## Validated

The gate scripts are exercised end-to-end against real nginx with
`lua-nginx-module` and `lua-resty-openidc`: the TOTP implementation matches the
RFC 6238 test vectors, a full login sets a signed session cookie and reaches
the upstream, brute-force lockout trips after ten failures, and an unreachable
IdP opens the circuit and serves the TOTP fallback. The one path that needs
your own environment is a successful OIDC login against a live IdP.
