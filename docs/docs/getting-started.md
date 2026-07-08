import CodeBlock from "@theme/CodeBlock";
import lightCompose from "!!raw-loader!@site/../docker-compose.yml";
import lightEnv from "!!raw-loader!@site/../.env.example";
import fullCompose from "!!raw-loader!@site/../example/full/docker-compose.yml";

# Getting started

The quickest way to run Lightngx is with Docker Compose.

```sh
mkdir -p nginx/conf nginx/logs lightngx
cp .env.example .env          # optional, every value has a default
docker compose up -d
```

Open the UI on **port 9000**. On the first run it shows a setup page where you
create the first administrator.

This is the `docker-compose.yml` those commands use. Every setting reads from
an environment variable with a sensible default, so an untouched copy just
works and `.env` only holds what you change.

<CodeBlock language="yaml" title="docker-compose.yml">{lightCompose}</CodeBlock>

<details>
<summary>The matching <code>.env.example</code></summary>

<CodeBlock language="ini" title=".env.example">{lightEnv}</CodeBlock>

</details>

## Light or full image

The default `ghcr.io/buco7854/lightngx:latest` (`:light`) is nginx plus the
Lightngx binary and nothing else, which is all most setups need. The
`:full` tag adds an in-nginx CrowdSec WAF bouncer, traffic stats, and a lua
runtime for OIDC/TOTP auth gates. Pick `:full` only if you want those
extras. See [Light and full images](./images.md) for what each turns on and
a complete example stack.

## Pre-seeding the first admin

If you would rather not use the setup page, generate a bcrypt hash and pass it
in. The setup page is then skipped.

```sh
docker run --rm -i ghcr.io/buco7854/lightngx:latest lightngx hash
```

Set `LN_ADMIN_USER` and `LN_ADMIN_PASSWORD_HASH` from the result. A password
change made later in the app is kept.

## Where your data lives

`/etc/nginx` is seeded from the image template on the first start, but only
when the bind mount is empty. After that Lightngx never touches it on its own.

Accounts and settings live in an SQLite file under the data directory
(`lightngx/` in the example above). Keep that volume if you want your users,
sessions and settings to survive a container rebuild.

:::tip Keep sessions valid across rebuilds
Set `LN_SESSION_SECRET` to a fixed value of 32 or more characters so session
cookies survive a container recreation. Left unset it is generated at each
start, which logs everyone out on restart. Stored TOTP secrets are encrypted
with a separate key kept in the data directory, so keep that volume to
preserve two-factor enrollments.
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

Terminate TLS at the proxy, keep `LN_SECURE_COOKIES=true`, and set
`LN_TRUSTED_PROXIES` to the proxy address so forwarded client IPs are trusted
for audit logs and rate limiting. See [Security](./security.md) for the
details.

WebAuthn needs a stable hostname. It works when you reach the UI directly or
through a proxy that preserves the `Host` header, but not over a bare IP
address. Use `localhost` or a real domain.
