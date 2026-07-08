# Getting started

The quickest way to run Lightngx is with Docker Compose.

```sh
mkdir -p nginx/conf nginx/logs lightngx
cp .env.example .env          # optional, every value has a default
docker compose up -d
```

Open the UI on **port 9000**. On the first run it shows a setup page where you
create the first administrator.

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
Set `LN_SESSION_SECRET` to a fixed value of 32 or more characters. It signs
sessions and also keys the encryption of stored TOTP secrets, so pinning it
keeps both valid when the container is recreated. Left unset, it is generated
at each start, which logs everyone out on restart.
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
