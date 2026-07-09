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

Four steps, each a block you copy in turn: fetch the stack and start it (this
seeds `./nginx/conf`, which is `/etc/nginx`), install the gate into the seeded
config, point it at your domain, then reload. Every file is also shown in full,
so you can paste it by hand instead of fetching.

:::note Already run your own nginx?
Skip step 1. You still need the three gate `*.lua` on your nginx's lua require
path (`/usr/local/share/lua/5.1/`, a bind mount or your image) and the `:full`
image (or your own built from it) for the lua runtime. Save the files from steps
2-3 under your own `/etc/nginx`, and use `nginx -t && nginx -s reload` (after
chowning the gate keys to the worker user). Without CrowdSec, also add
`01-gate-resolver.conf` (see [Does it
conflict?](#does-it-conflict-with-the-config-lightngx-seeds)) for `resolver` and
TLS trust; with CrowdSec its seeded config already provides them.
:::

### 1. Fetch the stack and start it

The gate `*.lua` must exist before the first start (the compose mounts them), so
fetch everything and generate the secrets, then start:

```sh
mkdir lightngx && cd lightngx
base=https://raw.githubusercontent.com/buco7854/lightngx/main/example/hardened
curl -fsSL $base/docker-compose.yml -o docker-compose.yml
mkdir -p crowdsec/conf nginx/lua
curl -fsSL $base/crowdsec/conf/config.yaml.local -o crowdsec/conf/config.yaml.local
for f in oidc_gate.lua totp_gate.lua totp.lua; do curl -fsSL "$base/nginx/lua/$f" -o "nginx/lua/$f"; done
{ echo "CROWDSEC_BOUNCER_KEY=$(openssl rand -hex 16)"
  echo "CROWDSEC_DB_PASSWORD=$(openssl rand -hex 16)"
  echo "LN_SESSION_SECRET=$(openssl rand -hex 32)"; } > .env
docker compose up -d
```

This seeds `./nginx/conf` and runs nginx without the gate for now. The gate
`*.lua` are plain lua you own and can edit.

<details>
<summary>The stack files, to read or paste by hand</summary>

The compose is the full example plus the three gate `*.lua` mounts:

<CodeBlock language="yaml" title="docker-compose.yml">{hardenedCompose}</CodeBlock>

<CodeBlock language="ini" title=".env">{hardenedEnv}</CodeBlock>

CrowdSec reads its DB connection from this mounted `config.yaml.local` (the
image has no `DB_*` env, so without it CrowdSec silently uses SQLite and ignores
Postgres):

<CodeBlock language="yaml" title="crowdsec/conf/config.yaml.local">{crowdsecLocal}</CodeBlock>

The three gate scripts (under `nginx/lua/`):

<CodeBlock language="lua" title="nginx/lua/oidc_gate.lua">{oidcLua}</CodeBlock>

<CodeBlock language="lua" title="nginx/lua/totp_gate.lua">{totpGateLua}</CodeBlock>

<CodeBlock language="lua" title="nginx/lua/totp.lua">{totpLua}</CodeBlock>

</details>

### 2. Install the gate into the seeded config

Now `./nginx/conf` exists. Add the gate's shared dictionaries, the gate snippet,
the public vhost, and the generated key files. Pick one block.

For an **OIDC gate** (authenticate at your IdP, with a TOTP fallback):

```sh
base=https://raw.githubusercontent.com/buco7854/lightngx/main/example/hardened
mkdir -p nginx/conf/conf.d nginx/conf/snippets nginx/conf/gates/totp nginx/conf/gates/oidc
curl -fsSL $base/conf.d/00-auth-gate.conf              -o nginx/conf/conf.d/00-auth-gate.conf
curl -fsSL $base/snippets/oidc-gate.conf               -o nginx/conf/snippets/oidc-gate.conf
curl -fsSL $base/conf.d/10-lightngx-gated.conf.example -o nginx/conf/conf.d/10-lightngx-gated.conf
head -c 20 /dev/urandom | base32 > nginx/conf/gates/totp/secret         # TOTP fallback secret
head -c 32 /dev/urandom          > nginx/conf/gates/totp/cookie_key     # signs the gate cookie
head -c 32 /dev/urandom          > nginx/conf/gates/oidc/session_secret # encrypts the OIDC cookie
```

For a **TOTP-only gate** (no IdP):

```sh
base=https://raw.githubusercontent.com/buco7854/lightngx/main/example/hardened
mkdir -p nginx/conf/conf.d nginx/conf/snippets nginx/conf/gates/totp
curl -fsSL $base/conf.d/00-auth-gate.conf              -o nginx/conf/conf.d/00-auth-gate.conf
curl -fsSL $base/snippets/totp-gate.conf               -o nginx/conf/snippets/totp-gate.conf
curl -fsSL $base/conf.d/10-lightngx-gated.conf.example -o nginx/conf/conf.d/10-lightngx-gated.conf
head -c 20 /dev/urandom | base32 > nginx/conf/gates/totp/secret       # shared TOTP secret
head -c 32 /dev/urandom          > nginx/conf/gates/totp/cookie_key   # signs the gate cookie
```

<details>
<summary>The gate config files, to read or paste by hand</summary>

`00-auth-gate.conf` declares the gate's shared memory (http context, so `conf.d/`):

<CodeBlock language="nginx" title="nginx/conf/conf.d/00-auth-gate.conf">{gateSetup}</CodeBlock>

The OIDC snippet. `redirect_uri` is just the path `/__oidc_callback`, which
lua-resty-openidc expands to `https://<host>/__oidc_callback` from the request,
so there is no domain to hardcode; register that URL as an allowed redirect URI
at your IdP:

<CodeBlock language="nginx" title="nginx/conf/snippets/oidc-gate.conf">{oidcSnippet}</CodeBlock>

The TOTP snippet (self-contained, no IdP):

<CodeBlock language="nginx" title="nginx/conf/snippets/totp-gate.conf">{totpSnippet}</CodeBlock>

The public vhost, which `include`s the gate:

<CodeBlock language="nginx" title="nginx/conf/conf.d/10-lightngx-gated.conf">{gatedVhost}</CodeBlock>

</details>

### 3. Point the gate at your domain

Edit these before reloading:

- **`nginx/conf/conf.d/10-lightngx-gated.conf`** — set `server_name` and the
  `ssl_certificate` paths to your domain and certificates. For a **TOTP-only**
  gate, also change its `include` line to `snippets/totp-gate.conf`.
- **OIDC only** — in `nginx/conf/snippets/oidc-gate.conf` set `discovery` and
  `client_id` for your IdP, then save the client secret it issued:

```sh
printf '%s' 'YOUR_OIDC_CLIENT_SECRET' > nginx/conf/gates/oidc/client_secret
```

Enroll the TOTP secret in your authenticator app (Aegis, Google Authenticator,
1Password, …), pasting the string from `nginx/conf/gates/totp/secret`:

```
otpauth://totp/Lightngx?secret=<the base32 secret>&issuer=Lightngx
```

For a LAN shortcut past the gate, copy `/usr/share/lightngx/examples/ui-proxy.conf`
into `conf.d`: a private-network proxy to the UI on `:9001`, on a different port.

### 4. Reload

```sh
docker compose exec nginx nginx -t   # check the config first
docker compose restart nginx         # owns /etc/nginx as the worker user, then loads the gate
```

The restart chowns `/etc/nginx` to the nginx **worker** user (so the workers can
read the gate keys) and loads the gate in one step. Browse your domain: you now
pass the IdP (or a TOTP code) before you ever reach Lightngx's own login.

Keep `.env` and everything under `nginx/conf/gates` (the keys) out of any git
repo; they are secrets.

<details>
<summary>Applying the key ownership with a live reload instead of a restart</summary>

The keys must be owned by the user nginx runs its workers as (the `user`
directive in `nginx.conf`); a restart does this for you. To instead
`nginx -s reload` without restarting, run the chown by hand from a container so
the name resolves to the right uid:

```sh
docker compose run --rm --no-deps --entrypoint sh nginx \
  -c 'chown -R nginx:nginx /etc/nginx && chmod -R go-rwx /etc/nginx/gates'
docker compose exec nginx nginx -t && docker compose exec nginx nginx -s reload
```

Swap `nginx:nginx` for `www-data:www-data` if that is your `user` directive
(`grep -E '^\s*user' nginx/conf/nginx.conf`). To turn the automatic ownership
off, set `LN_FIX_CONFIG_PERMS=false`.

</details>

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
