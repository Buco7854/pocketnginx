# Choosing a setup

Lightngx ships as two images, **light** and **full**, built from one Dockerfile.
The **hardened** setup is the full image with an nginx-level auth gate in front
of the UI. Here is what each has; pick the one that fits.

| Setup | Image | What it has | Good for |
| --- | --- | --- | --- |
| [Light](./light.md) | `:latest` (`:light`) | nginx plus the Lightngx UI, and nothing else | Plain reverse-proxy management. The smallest image |
| [Full](./full.md) | `:full` | Everything in light, plus an in-nginx CrowdSec WAF bouncer, traffic stats (VTS), and the lua runtime (`lua-resty-openidc`) for nginx-side auth gates | A WAF, traffic stats, or OIDC/TOTP gates in front of your upstreams |
| [Hardened](./hardened.md) | `:full` with a gate | The full image, with an OIDC/TOTP gate in front of the Lightngx login itself | Exposing the UI to the internet behind a second wall |

## What the full image includes

The light and full images are one Dockerfile with different `--target`s. There
is no on/off switch for the full extras: each turns on from its own required
input, and on the light image those inputs simply warn and do nothing, so a
misapplied variable can never break nginx.

- **CrowdSec bouncer** — an in-nginx WAF that bans bad actors at the edge. Turns
  on when you set `CROWDSEC_LAPI_KEY`.
- **Traffic stats (VTS)** — a per-host dashboard. The module loads but stays
  inert until you add a `vhost_traffic_status_zone` and a display vhost.
- **Auth-gate runtime** — lua-nginx-module with the whole `lua-resty-openidc`
  dependency tree on the lua path, so a `rewrite_by_lua_block` gate works with no
  extra wiring.

The [Full setup](./full.md) wires up CrowdSec and shows the VTS and gate extras.
The [Hardened setup](./hardened.md) uses that gate runtime to put an OIDC/TOTP
check in front of the Lightngx UI itself.

## Not using Docker?

The binary is a single static file with the frontend embedded; see [Running
without Docker](./without-docker.md).
