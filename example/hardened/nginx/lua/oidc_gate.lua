-- OIDC gate. Thin wrapper around lua-resty-openidc; meant to run in a
-- `rewrite_by_lua_block` (NOT access - would shadow the CrowdSec
-- bouncer at http context). See example/.../snippets/oidc-gate.conf
-- for usage and the README for the full doc.
--
-- Discovery: `oidc.discovery` accepts a URL (auto) or a table with
-- authorization_endpoint / token_endpoint / jwks_uri / issuer (manual).
--
-- Down detection - two paths:
--   1. In-band - any IdP call from nginx (cold discovery / JWKS /
--      token exchange) that fails opens the circuit.
--   2. Active probe - `ngx.timer.every` on worker 0, lazy-started on
--      first guarded request, polls discovery so outages are caught
--      even when nginx has a warm cache and would otherwise just 302
--      users into a dead IdP.
-- With `totp_fallback` set, an open circuit routes to
-- `totp_gate.guard(totp_fallback)`. Without it, IdP-down returns
-- 503 + Retry-After:30.

local _M = { _VERSION = "0.1" }

local DEFAULT_FALLBACK_TTL    = 30          -- seconds the circuit stays open
local DEFAULT_HEALTH_DICT     = "oidc_health"
local DOWN_KEY                = "oidc:down"
local DEFAULT_SCOPE           = "openid profile email"
local DEFAULT_PROBE_INTERVAL  = 30          -- seconds between active probes
local DEFAULT_PROBE_TIMEOUT   = 5           -- seconds per probe attempt
-- Status for "XHR has no session, please re-auth" - NOT 401.
-- CrowdSec's http-generic-bf scenario watches 401/403 and treats
-- SPA polling storms as brute-force attempts. 419 (Laravel's "Page
-- Expired") is recognised by most SPAs as "session expired, refresh
-- and re-auth", and isn't watched by any default CrowdSec scenario.
-- 401 stays reserved for actual credential failures (wrong code /
-- denied consent / etc.) where the brute-force counter SHOULD fire.
local XHR_UNAUTH_STATUS       = 419

local client_secret_cache  = {}             -- path -> string, per-worker
local session_secret_cache = {}             -- path -> string, per-worker
local whitelist_file_cache = {}             -- path -> {cidr,...}, per-worker
local probes_started       = {}             -- url -> true, per-worker

-- Errors we treat as "IdP infra broken" - open circuit + use fallback.
-- Anything else (denied consent, sig mismatch) surfaces as-is.
local INFRA_ERR_PATTERNS = {
    "accessing discovery",
    "accessing jwks",
    "accessing token endpoint",
    "accessing the userinfo endpoint",
    "could not contact",
    "could not get discovery",
    "could not get jwks",
    "could not get token",
    "connection refused",
    "connection reset",
    "connect: timeout",
    "timeout",
    "no resolver defined",
    "could not resolve",
    "unable to resolve host",
    "no route to host",
    "ssl handshake failed",
}

local function is_infra_error(err)
    if not err then return false end
    local e = tostring(err):lower()
    for _, pat in ipairs(INFRA_ERR_PATTERNS) do
        if e:find(pat, 1, true) then return true end
    end
    return false
end

-- Whitelist helpers (duplicated from totp_gate to keep the two
-- modules independently usable). IPv6 never matches → falls through.
local function ipv4_to_n(ip)
    local a, b, c, d = ip:match("^(%d+)%.(%d+)%.(%d+)%.(%d+)$")
    if not a then return nil end
    a, b, c, d = tonumber(a), tonumber(b), tonumber(c), tonumber(d)
    if a > 255 or b > 255 or c > 255 or d > 255 then return nil end
    return a * 16777216 + b * 65536 + c * 256 + d
end

local function ipv4_in_cidr(ip_n, cidr)
    if not ip_n then return false end
    local net, bits_s = cidr:match("^([%d%.]+)/(%d+)$")
    local bits = bits_s and tonumber(bits_s) or 32
    local net_n = ipv4_to_n(net or cidr)
    if not net_n or bits < 0 or bits > 32 then return false end
    if bits == 0 then return true end
    local shift = 2 ^ (32 - bits)
    return math.floor(ip_n / shift) == math.floor(net_n / shift)
end

local function ip_in_list(ip, list)
    if not list or not ip then return false end
    local n = ipv4_to_n(ip)
    if not n then return false end
    for _, cidr in ipairs(list) do
        if ipv4_in_cidr(n, cidr) then return true end
    end
    return false
end

-- Shared CIDR list from a file. Per-worker cached.
local function load_whitelist_file(path)
    if whitelist_file_cache[path] ~= nil then
        return whitelist_file_cache[path]
    end
    local f = io.open(path, "rb")
    if not f then
        ngx.log(ngx.WARN, "oidc_gate: whitelist_ips_file unreadable: ", path)
        whitelist_file_cache[path] = {}
        return whitelist_file_cache[path]
    end
    local list = {}
    for line in f:lines() do
        line = line:gsub("#.*$", ""):gsub("^%s+", ""):gsub("%s+$", "")
        if line ~= "" then list[#list + 1] = line end
    end
    f:close()
    whitelist_file_cache[path] = list
    return list
end

local function ip_allowed(ip, opts)
    if ip_in_list(ip, opts.whitelist_ips) then return true end
    if opts.whitelist_ips_file and
       ip_in_list(ip, load_whitelist_file(opts.whitelist_ips_file)) then
        return true
    end
    return false
end

local function path_in_list(path, list)
    if not list then return false end
    for _, pattern in ipairs(list) do
        if path:find(pattern) then return true end
    end
    return false
end

-- Cached per-worker. Workers run as `nginx` - chown the file
-- accordingly, or you get a Permission denied in the log.
local function load_client_secret(path)
    local v = client_secret_cache[path]
    if v then return v end
    local f, oerr = io.open(path, "rb")
    if not f then
        ngx.log(ngx.ERR, "oidc_gate: client_secret_file unreadable: ", path,
                " (", oerr or "unknown error", ") - workers run as non-root;",
                " try `chown nginx:nginx ", path, "`")
        return nil
    end
    local s = f:read("*a")
    f:close()
    s = s and s:gsub("^%s+", ""):gsub("%s+$", "") or ""
    if s == "" then
        ngx.log(ngx.ERR, "oidc_gate: client_secret_file empty: ", path)
        return nil
    end
    client_secret_cache[path] = s
    return s
end

-- Shared session secret for lua-resty-session - MUST be identical
-- across all workers or the OIDC callback fails with "no session
-- state found" (worker A signs the cookie, worker B can't decrypt).
-- lua-resty-session 4.x takes the first 32 bytes as the AES-256 key.
-- Generate with: head -c 32 /dev/urandom > /etc/nginx/gates/oidc/session_secret
local function load_session_secret(path)
    local v = session_secret_cache[path]
    if v then return v end
    local f, oerr = io.open(path, "rb")
    if not f then
        ngx.log(ngx.ERR, "oidc_gate: session_secret_file unreadable: ", path,
                " (", oerr or "unknown error", ") - chown nginx:nginx ", path)
        return nil
    end
    local s = f:read("*a")
    f:close()
    if not s or #s < 32 then
        ngx.log(ngx.ERR, "oidc_gate: session_secret_file too short - need ≥32B, got ",
                s and #s or 0, " (", path, ")")
        return nil
    end
    s = s:sub(1, 32)
    session_secret_cache[path] = s
    return s
end

local DEFAULT_FALLBACK_NOTICE =
    "Single sign-on is temporarily unavailable. " ..
    "Sign in with your authenticator code as a fallback. " ..
    "Verification may take a little longer than usual " ..
    "while this authentication method is confirmed."

-- Add a notice to the TOTP page so users see WHY they're being
-- asked for a code. Also propagates whitelist_ips_file and
-- totp_login_template_file (as totp_gate's login_template_file) from
-- the parent opts, so neither needs duplicating in totp_fallback.
-- An explicit totp_fallback.login_template_file still wins.
-- Shallow-copy so we don't mutate the user's snippet table.
local function fall_back_to_totp(opts, reason)
    ngx.log(ngx.WARN, "oidc_gate: TOTP fallback engaged (", reason, ")")
    local totp_opts = opts.totp_fallback
    local merged = { notice = DEFAULT_FALLBACK_NOTICE,
                     notice_kind = "fallback-oidc",
                     whitelist_ips_file = opts.whitelist_ips_file,
                     login_template_file = opts.totp_login_template_file }
    for k, v in pairs(totp_opts) do
        if merged[k] == nil then merged[k] = v end
    end
    if totp_opts.notice              then merged.notice              = totp_opts.notice              end
    if totp_opts.notice_kind         then merged.notice_kind         = totp_opts.notice_kind         end
    if totp_opts.whitelist_ips_file  then merged.whitelist_ips_file  = totp_opts.whitelist_ips_file  end
    if totp_opts.login_template_file then merged.login_template_file = totp_opts.login_template_file end
    return require("totp_gate").guard(merged)
end

-- Background probe. Worker 0 only, one timer per probe URL.
-- Catches outages even when nginx has a warm discovery cache and
-- would otherwise just 302 users into a dead IdP.
local function probe_idp(premature, url, timeout_ms, shd, ssl_verify, hold_ttl)
    if premature then return end
    local http_ok, http = pcall(require, "resty.http")
    if not http_ok then
        ngx.log(ngx.ERR, "oidc_gate: probe needs resty.http (", http, ")")
        return
    end
    local httpc = http.new()
    httpc:set_timeout(timeout_ms)
    local res, err = httpc:request_uri(url, {
        method     = "GET",
        ssl_verify = ssl_verify,
        keepalive  = false,
    })

    local up = res ~= nil and res.status and res.status < 500
    if up then
        -- Don't close the circuit here - a healthy discovery endpoint
        -- doesn't prove the full auth flow works. The circuit closes
        -- naturally: once the probe stops re-setting DOWN_KEY on
        -- failure, the existing TTL expires within 30-60s.
        if shd:get(DOWN_KEY) then
            ngx.log(ngx.NOTICE, "oidc_gate: probe ", url, " got HTTP ",
                    res.status, " - letting circuit TTL expire naturally")
        end
    else
        shd:set(DOWN_KEY, true, hold_ttl)
        ngx.log(ngx.WARN, "oidc_gate: probe ", url, " failed (",
                err or ("HTTP " .. tostring(res and res.status)),
                ") - circuit held open for ", hold_ttl, "s")
    end
end

-- Explicit opts.probe_url wins; otherwise derive from discovery.
local function resolve_probe_url(opts)
    if opts.probe_url then return opts.probe_url end
    local disc = opts.oidc.discovery
    if type(disc) == "string" then return disc end
    if type(disc) == "table"  then
        return disc.jwks_uri or disc.authorization_endpoint or disc.issuer
    end
    return nil
end

local function maybe_start_probe(opts, shd)
    -- Worker 0 only; others read the result from the shared dict.
    if ngx.worker.id() ~= 0 then return end

    local url = resolve_probe_url(opts)
    if not url then return end
    if probes_started[url] then return end
    probes_started[url] = true

    local interval   = opts.probe_interval or DEFAULT_PROBE_INTERVAL
    local timeout_ms = (opts.probe_timeout or DEFAULT_PROBE_TIMEOUT) * 1000
    local hold_ttl   = interval * 2
    local ssl_verify = (opts.oidc.ssl_verify or "yes") ~= "no"

    local ok, err = ngx.timer.every(interval, probe_idp,
                                     url, timeout_ms, shd, ssl_verify, hold_ttl)
    if not ok then
        ngx.log(ngx.ERR, "oidc_gate: ngx.timer.every failed: ", err,
                " - active probe disabled, in-band detection only")
        probes_started[url] = nil
        return
    end
    ngx.log(ngx.NOTICE, "oidc_gate: probe started for ", url,
            " every ", interval, "s")
end

local function service_unavailable(reason)
    ngx.log(ngx.ERR, "oidc_gate: 503 - ", reason)
    ngx.status = 503
    ngx.header["Retry-After"]   = "30"
    ngx.header["Content-Type"]  = "text/plain; charset=utf-8"
    ngx.header["Cache-Control"] = "no-store"
    ngx.print("503 Service Unavailable - identity provider is unreachable.\n")
    return ngx.exit(503)
end

-- True for XHR / fetch requests, false for top-level navigations and
-- asset loads. Following a cross-origin 302 to the IdP works fine on
-- navigations and on `<script>`/`<img>`/`<link>` loads (which use
-- Sec-Fetch-Mode: no-cors), but breaks on XHR/fetch - the browser
-- CORS-blocks the redirect chain because the IdP's /authorize has
-- no Access-Control-Allow-Origin. For SPAs the right answer is 401
-- so the JS can refresh-and-retry instead of throwing a CORS error.
local function is_xhr_request()
    local mode = ngx.var.http_sec_fetch_mode
    return mode == "cors" or mode == "same-origin"
end

function _M.guard(opts)
    opts = opts or {}

    -- Strip client-supplied copies of the identity headers before any of
    -- the early returns below (IP/path whitelist, TOTP fallback session):
    -- every request that reaches the upstream must carry only headers this
    -- gate set itself.
    ngx.req.clear_header("X-Auth-Sub")
    ngx.req.clear_header("X-Auth-User")
    ngx.req.clear_header("X-Auth-Email")

    if ip_allowed(ngx.var.remote_addr, opts)             then return end
    if path_in_list(ngx.var.uri, opts.whitelist_paths)   then return end

    local shd = ngx.shared[opts.health_dict_name or DEFAULT_HEALTH_DICT]
    if shd and opts.oidc and opts.probe ~= false then
        maybe_start_probe(opts, shd)
    end

    if shd and shd:get(DOWN_KEY) then
        if opts.totp_fallback then
            return fall_back_to_totp(opts, "circuit open")
        end
        return service_unavailable("circuit open, no totp_fallback configured")
    end

    -- Honor existing TOTP sessions even when the circuit is closed.
    -- Prevents kicking out TOTP-authenticated users when the probe
    -- stops re-opening the circuit (recovery or cached response).
    if opts.totp_fallback then
        local totp_gate = require("totp_gate")
        if totp_gate.has_valid_session(opts.totp_fallback) then return end
    end

    local oidc_opts = opts.oidc
    if type(oidc_opts) ~= "table" or oidc_opts.discovery == nil then
        ngx.log(ngx.ERR, "oidc_gate: opts.oidc.discovery is required",
                " (URL for auto-discovery, or table for manual)")
        return ngx.exit(500)
    end

    if opts.client_secret_file and not oidc_opts.client_secret then
        local secret = load_client_secret(opts.client_secret_file)
        if not secret then return ngx.exit(500) end
        oidc_opts.client_secret = secret
    end

    -- Shared session secret across all workers - required for the
    -- callback to find the state cookie set during the redirect-out.
    -- lua-resty-openidc expects this in its FOURTH positional arg
    -- (session_or_opts) - opts.session_opts is NOT read by current
    -- versions, despite older docs/examples suggesting otherwise.
    local session_or_opts = oidc_opts.session_opts
    if opts.session_secret_file then
        local secret = load_session_secret(opts.session_secret_file)
        if not secret then return ngx.exit(500) end
        session_or_opts = session_or_opts or {}
        if not session_or_opts.secret then
            session_or_opts.secret = secret
        end
    end

    oidc_opts.scope      = oidc_opts.scope      or DEFAULT_SCOPE
    oidc_opts.ssl_verify = oidc_opts.ssl_verify or "yes"
    oidc_opts.timeout    = oidc_opts.timeout    or (DEFAULT_PROBE_TIMEOUT * 1000)
    -- Slim default session contents - only the userinfo claims, which
    -- is all this gate forwards (X-Auth-Sub/User/Email). Avoids the
    -- 4 KB cookie limit triggering `session=` + `session2=` chunking,
    -- which Cloudflare (and some other proxies) mangles → auth works
    -- on the first request, every subsequent one bounces back to the
    -- IdP. Set explicitly in opts.oidc.session_contents to override
    -- (e.g. `{ user = true, access_token = true }` if you proxy the
    -- access token to upstream).
    if oidc_opts.session_contents == nil then
        oidc_opts.session_contents = { user = true }
    end

    -- SPA-safe unauth handling - see is_xhr_request() above. Browsers
    -- send Sec-Fetch-Mode: cors / same-origin on fetch/XHR; if we 302
    -- those cross-origin to the IdP the redirect chain hits the IdP's
    -- /authorize, which has no CORS headers, browser blocks. `deny`
    -- makes lua-resty-openidc return an error instead of redirecting;
    -- we turn that into a clean 401 + JSON so the SPA can refresh /
    -- re-prompt without a CORS storm.
    local xhr = is_xhr_request()
    local unauth_action
    if xhr then
        oidc_opts.unauth_action = "deny"
        unauth_action = "deny"
    end

    local openidc = require("resty.openidc")
    local res, err = openidc.authenticate(oidc_opts, nil, unauth_action, session_or_opts)

    if err then
        if is_infra_error(err) then
            local ttl = opts.fallback_ttl or DEFAULT_FALLBACK_TTL
            if shd then shd:set(DOWN_KEY, true, ttl) end
            ngx.log(ngx.ERR, "oidc_gate: OIDC unreachable: ", err,
                    " - circuit opened for ", ttl, "s")
            if opts.totp_fallback then
                return fall_back_to_totp(opts,
                                          "infra error: " .. tostring(err))
            end
            return service_unavailable(tostring(err))
        end
        if xhr then
            -- 419 (not 401) - see XHR_UNAUTH_STATUS comment at the
            -- top. SPA polling can't trip CrowdSec's bf scenarios.
            ngx.status = XHR_UNAUTH_STATUS
            ngx.header["Content-Type"]  = "application/json"
            ngx.header["Cache-Control"] = "no-store"
            ngx.header["WWW-Authenticate"] = 'OIDC realm="' ..
                (oidc_opts.client_id or "nginx-gate") .. '"'
            ngx.print('{"error":"session_required","reason":"',
                      tostring(err):gsub('"', "'"), '"}\n')
            return ngx.exit(XHR_UNAUTH_STATUS)
        end
        -- Non-XHR authentication failure: callback state mismatch,
        -- replay, expired pre-auth cookie, ID-token signature fail,
        -- etc. - the OIDC equivalent of "wrong credentials". 401 (not
        -- 500) so CrowdSec's http-generic-bf can catch anyone
        -- hammering /__oidc_callback with garbage codes / forged
        -- state. Matches totp_gate's failed-login 401 emit.
        ngx.log(ngx.WARN, "oidc_gate: callback/auth failed (", err,
                ") from ", ngx.var.remote_addr or "unknown")
        ngx.status = 401
        ngx.header["Content-Type"]  = "text/plain; charset=utf-8"
        ngx.header["Cache-Control"] = "no-store"
        ngx.print("401 Unauthorized - OIDC authentication failed.\n")
        return ngx.exit(401)
    end

    if shd then shd:delete(DOWN_KEY) end

    -- Upstream MUST be reachable only via this gate, otherwise these
    -- forged headers let anyone impersonate any user.
    if res and opts.set_user_headers ~= false then
        local id = res.id_token or res.user or {}
        if id.sub                then ngx.req.set_header("X-Auth-Sub",   id.sub)                end
        if id.preferred_username then ngx.req.set_header("X-Auth-User",  id.preferred_username) end
        if id.email              then ngx.req.set_header("X-Auth-Email", id.email)              end
    end
end

return _M
