import CodeBlock from "@theme/CodeBlock";
import fullCompose from "!!raw-loader!@site/../example/full/docker-compose.yml";
import fullEnv from "!!raw-loader!@site/../example/full/.env.example";
import vtsVhost from "!!raw-loader!@site/../example/full/conf.d/20-vhost-traffic-status.conf";
import crowdsecLocal from "!!raw-loader!@site/../example/full/crowdsec/conf/config.yaml.local";

# Light and full images

One Dockerfile builds two flavours, selected with `--target`.

| Tag | Contents | For |
| --- | --- | --- |
| `ghcr.io/buco7854/lightngx:latest` (also `:light`) | nginx plus the lightngx binary, and nothing else | Plain reverse-proxy management, smallest image |
| `ghcr.io/buco7854/lightngx:full` | Everything in light, plus the CrowdSec lua bouncer, nginx-module-vts, and the lua runtime with `lua-resty-openidc` for nginx-side auth gates | Stacks that want an in-nginx WAF, traffic stats, or OIDC and TOTP gates in front of upstreams |

There is no on/off switch for the extras. Each feature turns on from its own
required input, and on the light image those inputs simply warn and do nothing.
A misapplied variable can never break nginx.

## CrowdSec bouncer (full)

The full image compiles NDK, lua-nginx-module (with the OpenResty LuaJIT) and
nginx-module-vts against the exact nginx it ships, and installs the CrowdSec
nginx bouncer from packagecloud with a pinned GPG fingerprint. A build-time
`nginx -t` loads every module, so upstream breakage fails the build instead of
shipping silently.

The bouncer turns on when you set `CROWDSEC_LAPI_KEY`. At startup the entrypoint
links the lua modules, seeds the bouncer snippet, resolver drop-in and ban and
captcha templates, and writes `CROWDSEC_LAPI_URL` (optional) and
`CROWDSEC_LAPI_KEY` into the bouncer config. With no key, nginx runs plain. The
entrypoint only ever creates what is missing, and never overwrites a file you
have edited.

## Traffic stats with VTS (full)

The full image loads the VTS module by default, but configures nothing else for
it: no zone, no dashboard, no vhost. The module stays inert until you add a
`vhost_traffic_status_zone` and a `vhost_traffic_status_display` server to your
own config. Once you do, it serves a Lightngx-styled dashboard that is compiled
into the module by default.

The `example/full/` stack ships a ready-to-use vhost that serves the dashboard
on a private `:9113`. Copy it into your seeded config
(`nginx/conf/conf.d/20-vhost-traffic-status.conf`) and uncomment the matching
`127.0.0.1:9113:9113` port bind in the compose:

<CodeBlock language="nginx" title="example/full/conf.d/20-vhost-traffic-status.conf">{vtsVhost}</CodeBlock>

:::warning Do not load VTS twice
If your config already has its own `load_module` for VTS, remove it. A duplicate
load fails `nginx -t`.
:::

You have three options for the dashboard:

- Ship the stock nginx-module-vts page instead by building with
  `--build-arg VTS_STOCK_DASHBOARD=1`.
- Bake your own at build time by placing it at `docker/vts/status.html`.
- Swap it at runtime with no rebuild: serve your own HTML at the display
  location's exact URI, and let VTS keep the `format/*` and `control`
  sub-paths. The details are in
  [docker/vts/README.md](https://github.com/buco7854/lightngx/blob/main/docker/vts/README.md).

## Auth gates in front of your upstreams (full)

The full image carries the runtime to gate any `server{}` or `location{}`
behind an OIDC or TOTP check before requests reach the proxied app:
lua-nginx-module with LuaJIT, and the whole `lua-resty-openidc` dependency tree,
on the default lua path. lua loads by default on the full image (like VTS), so
a `rewrite_by_lua_block` gate works with no CrowdSec and no extra module wiring;
it stays inert until you add one.

The gate scripts are yours. Mount your own `*_gate.lua` and `include` it per
vhost with `rewrite_by_lua_block`, so it coexists with the CrowdSec bouncer.
The [hardened setup](./hardened.md) is a complete, ready-to-run example of
exactly this: an OIDC gate with a TOTP fallback (and a standalone TOTP gate)
put in front of the Lightngx UI itself.

## Example stack

A full-image reverse proxy with the CrowdSec bouncer wired up. CrowdSec
itself (the LAPI and its Postgres database) runs alongside; Lightngx registers
as a bouncer with the key you generate. This is a trimmed version of a working
homelab stack; add a firewall bouncer, a CrowdSec dashboard or a cert manager
as you see fit.

You do not need to clone anything. Save this as `docker-compose.yml` in an empty
directory:

<CodeBlock language="yaml" title="docker-compose.yml">{fullCompose}</CodeBlock>

Save this as `.env` beside it and fill the two required secrets; everything else
has a default. The bouncer key is any random string CrowdSec registers on first
boot, and Lightngx authenticates with the same value. Everything else about the
bouncer (ban and captcha templates, the resolver drop-in) is seeded
automatically on start. See
[Configuration](./configuration.md#crowdsec-full-image) for the two CrowdSec
variables.

<CodeBlock language="ini" title=".env">{fullEnv}</CodeBlock>

CrowdSec reads its database connection from a mounted `config.yaml.local` (the
image has no `DB_*` environment variables, so without this file it silently falls
back to SQLite and ignores the Postgres service). Save this as
`crowdsec/conf/config.yaml.local`; it takes the DB credentials from the same
`.env`:

<CodeBlock language="yaml" title="crowdsec/conf/config.yaml.local">{crowdsecLocal}</CodeBlock>

To automate certificates, point a cert manager (Certbot, CertWarden, acme.sh,
…) at your certificate directory and have it reload nginx through a scoped
[API key](./api-keys.md) with the `nginx:reload` scope.
