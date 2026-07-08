# System requirements

Lightngx is a single static Go binary with the frontend and an embedded
SQLite database inside, so the list is short.

## Server

- **Linux.** The container images are published for amd64 and arm64. The
  bare binary builds for any platform Go supports, but only Linux is used
  and tested.
- **nginx.** Any reasonably recent version. The UI talks to nginx through
  its binary (`nginx -t`), its pidfile and signals, not through a plugin,
  so there is nothing to compile into nginx itself.
- **The Debian `sites-available` layout** for the Sites and Streams pages:
  vhost files in one directory, symlinks in another. The directories are
  configurable, and both pages can be turned off if you organize your
  config differently. The editor, logs and nginx controls work regardless.
- **Privileges.** Lightngx needs to read and write the nginx config
  directory, signal the nginx master process, and read the log files. In
  practice that means running it as root or as the same user that owns
  nginx and its config.

For the container route you additionally need Docker (or a compatible
runtime) with Compose v2. There is no database server, message queue or
other sidecar; the measured footprint below is the whole cost.

## Measured footprint

Numbers from a real run on a 4 vCPU Linux amd64 container, serving 1,000
sites (500 of them enabled), two streams, and a 19 MB access log of
200,000 lines. Treat them as ballpark figures, not guarantees.

| Measure | Value |
| --- | --- |
| Binary size, frontend and SQLite included | 19 MB |
| SQLite database with a few users, passkeys and API keys | 44 KB |
| Data directory total, database plus session key | 76 KB |
| Cold start until the UI serves requests | 15 ms |
| Memory at idle with the 1,000 sites configured | 13 MB resident |
| Memory at peak during the load below | 47 MB resident |
| Read throughput during the load | about 1,200 req/s |
| Guarded config saves during the load | about 40 saves/s |
| Guarded save latency, `nginx -t` over 500 sites included | 87 ms median, 169 ms p95 |

The load run: three signed-in users working at once for 60 seconds
through 24 readers paging the access log at random offsets, listing the
thousand sites, walking the config tree and opening site files, 4
editors saving config edits (every save runs the full `nginx -t` and
rolls back on failure), and 25 live log streams following a file being
appended to.

## Browser

Any current browser works, on desktop and mobile. WebAuthn (security keys
and passkeys) additionally requires a secure context: HTTPS, or plain HTTP
on `localhost` only, and a stable hostname rather than a bare IP address.

## Building from source

Only needed if you build the binary yourself instead of pulling an image:

- Go 1.25 or newer
- Node.js 22 or newer (to build the embedded frontend once)

See [Running without Docker](./without-docker.md) for the steps.
