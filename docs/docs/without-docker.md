# Running without Docker

The binary is fully static (pure-Go SQLite, `CGO_ENABLED=0`), so it runs on
any Linux box next to your existing nginx: no container runtime, no shared
libraries, no database server.

:::warning An honest caveat
I run Lightngx in its container, so this bare-metal path has not seen real
production use yet. Everything below is how it is designed to work and it
should work. But if something misbehaves, please
[open an issue](https://github.com/buco7854/lightngx/issues) so it can be
fixed.
:::

What you give up compared to the images: the [full image's](./images.md)
extras (CrowdSec bouncer, VTS traffic stats, the lua runtime for auth
gates) are built into that image's nginx and do not apply to your distro's
nginx, and there is no baked-in supervision or logrotate wiring: your
init system and the distro's logrotate keep those jobs.

## Build the binary

There are no prebuilt release binaries yet, so build from source. You need
Go 1.25+ and Node 22+ once, on any machine:

```sh
git clone https://github.com/buco7854/lightngx
cd lightngx
(cd web/app && npm ci && npm run build)   # frontend -> web/dist (embedded)
CGO_ENABLED=0 go build -trimpath ./cmd/lightngx
```

Copy the resulting `lightngx` binary to the server, for example to
`/usr/local/bin/lightngx`. It embeds everything; nothing else needs to be
deployed.

## First run

Create a data directory and seed the first admin (or skip the seeding and
use the first-run setup page in the browser):

```sh
sudo mkdir -p /var/lib/lightngx
lightngx hash    # prompts for a password, prints a bcrypt hash
```

Then start it with your paths. On a stock Debian or Ubuntu nginx the
defaults already match, so this is often enough:

```sh
sudo LN_ADMIN_USER=admin \
     LN_ADMIN_PASSWORD_HASH='<hash from above>' \
     lightngx
```

Open port 9000, log in, and put nginx behind it later as described in
[Getting started](./getting-started.md#running-behind-a-front-proxy). The
[Configuration](./configuration.md) page lists every variable if your
nginx lives somewhere else.

## A systemd unit

```ini
[Unit]
Description=Lightngx nginx web UI
After=network.target nginx.service

[Service]
ExecStart=/usr/local/bin/lightngx
Environment=LN_LISTEN=127.0.0.1:9000
# Environment=LN_ADMIN_USER=admin
# Environment=LN_ADMIN_PASSWORD_HASH=...
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

Save as `/etc/systemd/system/lightngx.service`, then
`systemctl enable --now lightngx`.

## Reload and restart semantics

Leave `LN_SUPERVISE` at its default `false` when systemd manages nginx.
In this mode:

- **Test** and **Reload** work exactly as in the container: `nginx -t`,
  then SIGHUP to the master process from the pidfile.
- **Restart** sends SIGQUIT to the master and expects the service manager
  to bring nginx back. A stock `nginx.service` does not restart after a
  clean quit, so either stick to Reload (which covers config changes), or
  add a drop-in so systemd respawns it:

```ini
# /etc/systemd/system/nginx.service.d/restart.conf
[Service]
Restart=always
RestartSec=2
```

Alternatively set `LN_SUPERVISE=true` and let Lightngx run nginx as its
own supervised child, like the container does. Then disable
`nginx.service` so the two do not fight over the master process.
