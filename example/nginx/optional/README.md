# Optional extras

Nothing here is provisioned by the image - not by default, not behind a
toggle. These are copy-paste starting points for choices only you can
make for your deployment.

## realip.conf - real client IPs behind NAT or a proxy

Restores client IPs from `X-Forwarded-For`. Trusting that header is a
security decision: anything able to reach nginx from a trusted range can
spoof its client IP, which then flows into logs, CrowdSec decisions and
VTS stats. If you run behind Docker bridge NAT or a fronting proxy and
accept that trade-off, review the ranges and drop the file in:

```sh
cp realip.conf <your nginx conf>/conf.d/realip.conf
```

Then reload nginx (or use the UI - the save is `nginx -t` guarded).

## vts-dashboard/ - custom VTS status page

A self-contained replacement for the stock VTS dashboard (per-host
filtering, pause, mobile layout). The page is compiled into the module,
so baking it is a build-time step:

```sh
cp vts-dashboard/status.html <repo>/docker/vts/status.html
docker build -t lightngx <repo>
```

## Custom CrowdSec ban / captcha pages

With `CROWDSEC_LAPI_KEY` set the stock templates are seeded into
`/var/lib/crowdsec/lua/templates` only when missing - your own files are
never overwritten. To customize, drop your `ban.html` / `captcha.html`
into that directory (or its bind mount) and they take precedence.
