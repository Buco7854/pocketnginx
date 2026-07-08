# Configuration

Everything is configured through environment variables, all prefixed with
`LN_`. Every value has a default, so an untouched setup runs fine.

## Core

| Variable | Default | Description |
| --- | --- | --- |
| `LN_LISTEN` | `:9000` | UI listen address |
| `LN_DATA_DIR` | `/var/lib/lightngx` | Location of the session key and SQLite database |
| `LN_DB_PATH` | `<data dir>/lightngx.db` | Override the SQLite file path |
| `LN_NGINX_BIN` | `nginx` | nginx binary |
| `LN_NGINX_CONF` | `/etc/nginx/nginx.conf` | Main config used by `nginx -t` |
| `LN_NGINX_CONF_DIR` | `/etc/nginx` | Editor root; nothing outside is readable or writable |
| `LN_NGINX_PID` | `/var/run/nginx.pid` | Pidfile, used when not supervising |
| `LN_SUPERVISE` | `false` (`true` in the image) | Run nginx as a supervised child process |
| `LN_LOGROTATE` | `true` | Rotate nginx logs on a timer while supervising |
| `LN_LOG_PATHS` | `/var/log/nginx` | Log files or directories, separated by comma or colon |
| `LN_MAX_EDIT_SIZE` | `2097152` | Largest editable file, in bytes |
| `LN_DOCKER_LOGS` | `false` | Keep the base image's stdout and stderr log symlinks; this disables the log viewer for those files |

## Accounts and MFA

| Variable | Default | Description |
| --- | --- | --- |
| `LN_ADMIN_USER` | | Optional seed admin username; setting it skips first-run setup |
| `LN_ADMIN_PASSWORD_HASH` | | bcrypt hash for the seed admin, generated with `lightngx hash` |
| `LN_MFA_REQUIRED_ROLES` | admin decides in-app | Pin the MFA policy, for example `admin` or `admin,user`. Empty means no requirement |
| `LN_WEBAUTHN_RPID` | request host | WebAuthn relying-party ID; defaults to the browser host |
| `LN_WEBAUTHN_ORIGINS` | request origin | Allowed WebAuthn origins, comma-separated |

## OIDC

| Variable | Description |
| --- | --- |
| `LN_OIDC_ISSUER` | Issuer URL, used for discovery |
| `LN_OIDC_CLIENT_ID` and `LN_OIDC_CLIENT_SECRET` | Client credentials |
| `LN_OIDC_REDIRECT_URL` | `https://<host>/api/auth/oidc/callback` |
| `LN_OIDC_SCOPES` | Defaults to `openid,profile,email` |
| `LN_OIDC_GROUPS_CLAIM` | ID-token claim holding the user's groups (default `groups`) |
| `LN_OIDC_ALLOWED_GROUPS` | Groups allowed to log in |
| `LN_OIDC_ADMIN_GROUPS` | Groups granted the admin role |

See [Accounts and access](./accounts.md#oidc) for how OIDC sign-in behaves and
how roles are assigned.

## Sessions and proxy

| Variable | Default | Description |
| --- | --- | --- |
| `LN_SESSION_SECRET` | auto-generated | 32 or more characters. It also keys TOTP-secret encryption, so set it to keep sessions and secrets valid across ephemeral containers |
| `LN_SESSION_TTL` | `12h` | Session lifetime |
| `LN_SECURE_COOKIES` | `true` | Set to `false` only for plain-HTTP testing |
| `LN_TRUSTED_PROXIES` | | CIDRs allowed to set `X-Forwarded-For`, used for audit logs and rate limiting behind a proxy |

## Sites and streams

| Variable | Default | Description |
| --- | --- | --- |
| `LN_SITES` | `true` | Sites page: enable, disable, maintenance, rename and delete |
| `LN_MAINTENANCE` | `true` | Maintenance mode buttons |
| `LN_MAINTENANCE_PAGE` | built-in page | Path to a custom maintenance HTML file |
| `LN_SITES_AVAILABLE_DIR` | `<conf dir>/sites-available` | Where available sites are stored |
| `LN_SITES_ENABLED_DIR` | `<conf dir>/sites-enabled` | Where enabled-site symlinks live |
| `LN_STREAMS` | `true` | Streams page for nginx `stream{}` vhosts |
| `LN_STREAMS_AVAILABLE_DIR` | `<conf dir>/streams-available` | Where available streams are stored |
| `LN_STREAMS_ENABLED_DIR` | `<conf dir>/streams-enabled` | Where enabled-stream symlinks live |

## CrowdSec (full image)

| Variable | Default | Description |
| --- | --- | --- |
| `CROWDSEC_LAPI_KEY` | | Bouncer API key. Setting it activates the CrowdSec bouncer and is written into the bouncer config |
| `CROWDSEC_LAPI_URL` | | LAPI URL written into the bouncer config when set, for example `http://crowdsec:8080` |

See [Light and full images](./images.md) for what the full image adds and how
the extras turn on.
