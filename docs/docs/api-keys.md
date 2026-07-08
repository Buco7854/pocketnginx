# API keys

Automation such as certificate-renewal hooks, CI jobs and monitoring can drive
nginx without a login by using scoped API keys. An admin creates and revokes
them on the **Administration** page.

Keys are confined to nginx operations. Each one grants a subset of
`nginx:status`, `nginx:test`, `nginx:reload` and `nginx:restart`, and nothing
more. Config editing, log access and account management stay session-only, so a
leaked key cannot be used to escalate.

Only a SHA-256 hash of the key is stored. The token itself is shown once, at
creation. Every call is recorded in the audit log as `apikey:<name>`.

Present the token as a bearer header, or as `X-API-Key`. A typical
post-issuance reload hook looks like this:

```sh
curl -fsS -XPOST https://nginx.example.com/api/nginx/reload \
  -H "Authorization: Bearer lngx_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
```
