import CodeBlock from "@theme/CodeBlock";
import lightCompose from "!!raw-loader!@site/../example/light/docker-compose.yml";
import lightEnv from "!!raw-loader!@site/../example/light/.env.example";
import uiProxy from "!!raw-loader!@site/../docker/ui-proxy.conf";
import uiProxyTls from "!!raw-loader!@site/../docker/ui-proxy-tls.conf";

# Getting started

Lightngx is a single container: nginx plus a web UI to manage it. The quickest
way to run it is with Docker Compose.

## Run it

You do not need to clone anything. Save this as `docker-compose.yml` in an empty
directory:

<CodeBlock language="yaml" title="docker-compose.yml">{lightCompose}</CodeBlock>

Every setting reads an environment variable that already has a sensible default,
so this runs as-is. To change any value, save a `.env` beside the compose; here
is every variable Lightngx reads, each at its default:

<details>
<summary>The full <code>.env</code> reference</summary>

<CodeBlock language="ini" title=".env">{lightEnv}</CodeBlock>

</details>

Then start it:

```sh
docker compose up -d
```

The compose publishes the UI on `127.0.0.1:9000`, so **on the machine running
it** open **`http://localhost:9000`**. The first visit shows a setup page where
you create the first administrator, and you are in.

That is the whole quick start. The rest of this page is reference: reaching the
UI from another machine, choosing an image, pre-seeding the admin, where data
lives, and running behind a proxy.

## Reaching the UI from another machine

:::warning The UI is not exposed by default
Port 9000 is bound to `127.0.0.1`, so nothing on your network reaches the UI
until you expose it. Do one of the two options below, or the UI stays
localhost-only.
:::

Cookies default to `auto`: the `Secure` flag follows the request scheme, so one
instance works over plain HTTP on the LAN and HTTPS from a front proxy at the
same time, with no env change.

- **Expose 9000 directly (local HTTP only).** Set `UI_BIND=0.0.0.0` in the
  compose and browse `http://<host>:9000` from your LAN. Do not put an external
  HTTPS proxy in front of this port: the app then can't tell the request was
  HTTPS and leaves the session cookie non-`Secure`. For any remote or HTTPS
  access, use the reverse-proxy option below instead.
- **Copy a proxy vhost into `conf.d`.** Two examples ship in the image at
  `/usr/share/lightngx/examples/`. The HTTP one answers private-network
  addresses only on `:9001`; publish that port (uncomment `- "9001:9001"`) and
  browse `http://<host>:9001` from your LAN:

<CodeBlock language="nginx" title="conf.d/lightngx.conf (HTTP, LAN only)">{uiProxy}</CodeBlock>

The HTTPS one terminates TLS. Set your domain and certificate paths first:

<CodeBlock language="nginx" title="conf.d/lightngx.conf (HTTPS)">{uiProxyTls}</CodeBlock>

For a public deployment, put an [auth gate](./hardened.md) in front.

## Light or full image

| Tag | Adds | Pick it when |
| --- | --- | --- |
| `:latest` (`:light`) | nginx + the Lightngx binary, nothing else | most setups; smallest image |
| `:full` | an in-nginx CrowdSec WAF bouncer, traffic stats (VTS), and the lua runtime for OIDC/TOTP auth gates | you want a WAF, stats, or an [auth gate](./hardened.md) |

See [Light and full images](./images.md) for what each extra turns on and a
complete example stack.

## Pre-seeding the first admin

To skip the setup page, generate a bcrypt hash and pass it in:

```sh
docker run --rm -i ghcr.io/buco7854/lightngx:latest lightngx hash
```

Set `LN_ADMIN_USER` and `LN_ADMIN_PASSWORD_HASH` from the result. A password
change made later in the app is kept.

## Where your data lives

`/etc/nginx` is seeded from the image template on the first start, but only when
the bind mount is empty. After that Lightngx never touches it on its own.

Accounts and settings live in an SQLite file under the data directory
(`lightngx/` in the example above). Keep that volume if you want your users,
sessions and settings to survive a container rebuild.

:::tip Keep sessions valid across rebuilds
Set `LN_SESSION_SECRET` to a fixed value of 32 or more characters so session
cookies survive a container recreation. Left unset it is generated at each
start, which logs everyone out on restart. Stored TOTP secrets are encrypted
with a separate key kept in the data directory, so keep that volume to preserve
two-factor enrollments.
:::

## Running behind a front proxy

Lightngx is designed to sit behind nginx itself. A minimal reverse proxy looks
like this:

```nginx
location / {
    proxy_pass http://127.0.0.1:9000;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;

    # The live log stream is server-sent events; do not buffer it.
    proxy_buffering off;
}
```

Terminate TLS at the proxy and set `LN_TRUSTED_PROXIES` to the proxy address so
forwarded client IPs (and, on a separate proxy, the forwarded scheme) are
trusted for audit logs, rate limiting and the `auto` cookie policy. See
[Security](./security.md) for the details.

WebAuthn needs a stable hostname. It works when you reach the UI directly or
through a proxy that preserves the `Host` header, but not over a bare IP
address. Use `localhost` or a real domain.

## Next steps

- [Sites and streams](./sites.md): manage vhosts and TCP/UDP proxies.
- [Accounts and access](./accounts.md): users, roles, two-factor and OIDC login.
- [Security](./security.md): how Lightngx is hardened, and how to expose it.
- [Hardened setup](./hardened.md): add an auth gate in front of the UI.
