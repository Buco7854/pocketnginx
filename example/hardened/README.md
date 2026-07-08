# Hardened deployment: an auth gate in front of the UI

An nginx-level authentication layer (an OIDC gate with a TOTP fallback, or a
standalone TOTP gate) plus the CrowdSec bouncer, in front of the Lightngx
login, using the lua runtime the `:full` image already ships. An
unauthenticated request never reaches the app.

**Setup and how it works: see the [Hardened setup
guide](https://buco7854.github.io/lightngx/hardened).**

## What's here

```
nginx/lua/           oidc_gate.lua, totp_gate.lua, totp.lua   (the only bind mount)
gates/whitelist_ips.example
snippets/            oidc-gate.conf, totp-gate.conf
conf.d/              00-auth-gate.conf              (shared dicts, always on)
                     01-gate-resolver.conf.example (only if NOT running CrowdSec)
                     10-lightngx-gated.conf.example (the public vhost, copy and edit)
                     20-vhost-traffic-status.conf   (optional VTS dashboard)
docker-compose.yml   the full image with just the lua mount, plus CrowdSec
```

Only the lua is bind-mounted (it lives outside `/etc/nginx`, so it sits in
`nginx/lua/` rather than the seeded `nginx/conf`). The snippets, the `conf.d/`
drop-ins and the gate key files (`gates/oidc/`, `gates/totp/`, which you create)
all go inside `nginx/conf`, the mounted config dir, once the first boot has
seeded it. The [setup guide](https://buco7854.github.io/lightngx/hardened) shows
exactly what to copy where. The gate scripts under `nginx/lua/` are plain lua
you own and can edit; each option is documented inline in the snippets and the
lua files.
