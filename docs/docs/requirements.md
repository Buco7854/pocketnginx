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
other sidecar: memory use is tens of megabytes.

## Browser

Any current browser works, on desktop and mobile. WebAuthn (security keys
and passkeys) additionally requires a secure context: HTTPS, or plain HTTP
on `localhost` only, and a stable hostname rather than a bare IP address.

## Building from source

Only needed if you build the binary yourself instead of pulling an image:

- Go 1.25 or newer
- Node.js 22 or newer (to build the embedded frontend once)

See [Running without Docker](./without-docker.md) for the steps.
