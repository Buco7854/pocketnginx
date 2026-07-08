import CodeBlock from "@theme/CodeBlock";
import fullCompose from "!!raw-loader!@site/../example/full/docker-compose.yml";
import fullEnv from "!!raw-loader!@site/../example/full/.env.example";

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
on the default lua path.

The gate scripts are yours. Mount your own `*_gate.lua` and `include` it per
vhost with `rewrite_by_lua_block`, so it coexists with the CrowdSec bouncer.
Lightngx ships no gate lua and seeds nothing for it.

## Example stack

A full-image reverse proxy with the CrowdSec bouncer wired up. CrowdSec
itself (the LAPI and its Postgres database) runs alongside; Lightngx registers
as a bouncer with the key you generate. This is a trimmed version of a working
homelab stack; add a firewall bouncer, a CrowdSec dashboard or a cert manager
as you see fit. It lives in the repo at
[`example/full/`](https://github.com/buco7854/lightngx/tree/main/example/full).

<CodeBlock language="yaml" title="example/full/docker-compose.yml">{fullCompose}</CodeBlock>

Copy `.env.example` to `.env` and fill the three required secrets; everything
else has a default. The bouncer key is any random string CrowdSec registers on
first boot, and Lightngx authenticates with the same value. Everything else
about the bouncer (ban and captcha templates, the resolver drop-in) is seeded
automatically on start. See
[Configuration](./configuration.md#crowdsec-full-image) for the two CrowdSec
variables.

<CodeBlock language="ini" title="example/full/.env.example">{fullEnv}</CodeBlock>

To automate certificates, point a cert manager (Certbot, CertWarden, acme.sh,
…) at your certificate directory and have it reload nginx through a scoped
[API key](./api-keys.md) with the `nginx:reload` scope.
