import CodeBlock from "@theme/CodeBlock";
import lightCompose from "!!raw-loader!@site/../example/light/docker-compose.yml";
import lightEnv from "!!raw-loader!@site/../example/light/.env.example";
import uiProxy from "!!raw-loader!@site/../docker/ui-proxy.conf";
import uiProxyTls from "!!raw-loader!@site/../docker/ui-proxy-tls.conf";

# Getting started

Lightngx is a single container: nginx plus a web UI to manage it. Run it with
Docker Compose.

## Run it

Copy this block into an empty directory. It fetches the compose, sets a session
secret so logins survive restarts, and starts the stack:

```sh
mkdir lightngx && cd lightngx
curl -fsSL https://raw.githubusercontent.com/buco7854/lightngx/main/example/light/docker-compose.yml -o docker-compose.yml
echo "LN_SESSION_SECRET=$(openssl rand -hex 32)" > .env
docker compose up -d
```

**On the machine running it**, open **`http://localhost:9000`**: the first visit
is a setup page where you create the first administrator.

<details>
<summary>Prefer to paste the compose by hand? Here it is.</summary>

<CodeBlock language="yaml" title="docker-compose.yml">{lightCompose}</CodeBlock>

</details>

## Change a setting

Everything else has a sensible default. The compose reads a few extras from that
`.env` beside it:

<CodeBlock language="ini" title=".env">{lightEnv}</CodeBlock>

[Configuration](./configuration.md) is the full list of `LN_*` variables. To use
one that is not in the `.env` above, add it to the compose's `environment:` list
(that is all a variable needs to take effect).

The rest of this page is reference: reaching the UI from another machine,
choosing an image, pre-seeding the admin, where data lives, and running behind a
proxy.

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
  compose and browse `http://<host>:9000` from your LAN.
- **Copy a proxy vhost into `conf.d`.** Two examples ship in the image at
  `/usr/share/lightngx/examples/`: an HTTP one (LAN-only on `:9001`, publish it
  with `- "9001:9001"`) and an HTTPS one that terminates TLS. Copy one out and
  reload:

```sh
docker compose cp nginx:/usr/share/lightngx/examples/ui-proxy.conf ./nginx/conf/conf.d/lightngx.conf
docker compose exec nginx nginx -s reload
```

<details>
<summary>The two example vhosts</summary>

<CodeBlock language="nginx" title="ui-proxy.conf (HTTP, LAN only, :9001)">{uiProxy}</CodeBlock>

<CodeBlock language="nginx" title="ui-proxy-tls.conf (HTTPS; set your domain + certs)">{uiProxyTls}</CodeBlock>

</details>

:::warning Expose 9000 directly only for local HTTP
Do not put an external HTTPS proxy straight in front of port 9000: the app
can't tell the request was HTTPS and leaves the session cookie non-`Secure`.
For any remote or HTTPS access, front it with the bundled nginx (the vhosts
above) instead, or set `LN_TRUSTED_PROXIES` to your proxy.
:::

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
