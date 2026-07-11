-- TOTP gate, drop-in from a `rewrite_by_lua_block` (NOT access, which would
-- shadow the CrowdSec bouncer at http context). See snippets/totp-gate.conf
-- for usage and the hardened README for the full doc.
--
-- Files (per-worker cached): /etc/nginx/gates/totp/secret (base32) and
-- /etc/nginx/gates/totp/cookie_key (32B+ random). Session cookie is
-- `<expiry>.<HMAC-SHA1(expiry)>`, HttpOnly + SameSite=Strict +
-- Secure-when-HTTPS.

local totp = require("totp")

local _M = { _VERSION = "0.1" }

local DEFAULT_SECRET_PATH     = "/etc/nginx/gates/totp/secret"
local DEFAULT_COOKIE_KEY_PATH = "/etc/nginx/gates/totp/cookie_key"
local DEFAULT_SESSION_TTL     = 8 * 3600  -- 8h
local DEFAULT_LOGIN_PATH      = "/__totp_login"
local DEFAULT_MAX_FAILURES    = 10         -- per-IP failures before lockout
local DEFAULT_FAILURE_WINDOW  = 15 * 60    -- rolling window for the counter
local DEFAULT_DICT_NAME       = "totp_ratelimit"
local COOKIE_NAME             = "totp_session"

local secret_cache     = {}  -- path -> string, per-worker
local cookie_key_cache = {}  -- path -> string, per-worker
local template_cache   = {}  -- path -> string | false (cache the miss too)
local whitelist_file_cache = {}  -- path -> {cidr,...}, per-worker
local rate_limit_warned = false  -- one NOTICE per worker when dict missing

local function read_trimmed(path)
    local f, oerr = io.open(path, "rb")
    if not f then return nil, oerr end  -- "Permission denied" / "No such file…"
    local s = f:read("*a")
    f:close()
    if not s then return nil, "read returned nil" end
    s = s:gsub("^%s+", ""):gsub("%s+$", "")
    if s == "" then return nil, "empty after trim" end
    return s
end

local function load_required(cache, path, label)
    local v = cache[path]
    if v then return v end
    local data, err = read_trimmed(path)
    if not data then
        ngx.log(ngx.ERR, "totp_gate: ", label, " unreadable: ", path,
                " (", err or "unknown error", ") - workers run as non-root;",
                " try `chown nginx:nginx ", path, "`")
        return ngx.exit(500)
    end
    cache[path] = data
    return data
end

local function b64url_encode(s)
    return (ngx.encode_base64(s):gsub("=", ""):gsub("%+", "-"):gsub("/", "_"))
end

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
    if not n then return false end  -- IPv6 / weird: never whitelisted by IP
    for _, cidr in ipairs(list) do
        if ipv4_in_cidr(n, cidr) then return true end
    end
    return false
end

-- Shared CIDR list from a file. One per line, # comments, blanks OK.
-- Per-worker cached; missing file -> empty + one WARN.
local function load_whitelist_file(path)
    if whitelist_file_cache[path] ~= nil then
        return whitelist_file_cache[path]
    end
    local f = io.open(path, "rb")
    if not f then
        ngx.log(ngx.WARN, "totp_gate: whitelist_ips_file unreadable: ", path)
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

local function sign_session(key, expiry)
    local payload = tostring(expiry)
    return payload .. "." .. b64url_encode(ngx.hmac_sha1(key, payload))
end

local function verify_session(key, value)
    if type(value) ~= "string" then return false end
    local payload, sig = value:match("^(%d+)%.([%w%-_]+)$")
    if not payload or not sig then return false end
    if b64url_encode(ngx.hmac_sha1(key, payload)) ~= sig then return false end
    local expiry = tonumber(payload)
    return expiry ~= nil and expiry > ngx.time()
end

-- Same-origin only - prevents open redirect via `?next=//evil`. Backslash
-- is rejected outright: browsers parse `\` as `/` in a Location header, so
-- `/\evil` would resolve to `//evil`.
local function safe_next(value)
    if type(value) ~= "string" then return "/" end
    if value:sub(1, 1) ~= "/" then return "/" end
    if value:sub(1, 2) == "//" then return "/" end
    if value:find("[%c\\]") then return "/" end
    return value
end

-- Baked default. A custom template (login_template_file, e.g. under
-- /etc/nginx/templates/) overrides it; without one, this is used.
-- Placeholders:
--   __ACTION__  (required) form action URL with ?next=…
--   __ERR__     error block on failed code / lockout, "" otherwise
--   __NOTICE__  info block (e.g. oidc_gate fallback), "" otherwise
local LOGIN_HTML = [[<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Authentication required</title><style>
*{box-sizing:border-box}body{font-family:system-ui,sans-serif;background:#111;
color:#eee;display:flex;align-items:center;justify-content:center;height:100vh;
margin:0}form{background:#1c1c1c;padding:2rem 2.5rem;border-radius:.5rem;
box-shadow:0 8px 24px rgba(0,0,0,.6);min-width:300px}h1{font-size:1rem;
margin:0 0 1rem;font-weight:500;color:#aaa;text-align:center}input{
font:600 1.5rem ui-monospace,monospace;letter-spacing:.3em;width:100%;
padding:.6rem .8rem;background:#0a0a0a;color:#fff;border:1px solid #333;
border-radius:.25rem;text-align:center}input:focus{outline:none;border-color:#3478f6}
button{margin-top:1rem;width:100%;padding:.7rem;background:#3478f6;color:#fff;
border:0;border-radius:.25rem;font:600 1rem system-ui,sans-serif;cursor:pointer}
button:hover{background:#2a68db}button:disabled{background:#555;cursor:wait}
.err{color:#ff6b6b;margin:.75rem 0 0;
font-size:.85rem;text-align:center}.notice{margin:0 0 1rem;padding:.55rem .8rem;
background:#3a3018;border:1px solid #6e5510;color:#f4d77e;border-radius:.25rem;
font-size:.8rem;line-height:1.4;text-align:center}</style></head>
<body><form method="POST" action="__ACTION__">__NOTICE__<h1>Enter your authenticator code</h1>
<input name="code" inputmode="numeric" pattern="[0-9]{6}" maxlength="6"
autocomplete="one-time-code" autofocus required>__ERR__
<button type="submit">Verify</button></form>
<script>document.querySelector('form').addEventListener('submit',function(){var b=this.querySelector('button');b.disabled=true;b.textContent='Verifying…'})</script>
</body></html>
]]

-- Per-worker cached. Missing/empty/broken templates silently fall
-- back to the baked default (warn-logged) so a typo can't lock you
-- out. `nginx -s reload` picks up changes.
local function load_template(path)
    -- No template configured: use the baked default, no file probe.
    if not path then return LOGIN_HTML end
    local cached = template_cache[path]
    if cached == false then return LOGIN_HTML end
    if cached then return cached end
    local f = io.open(path, "rb")
    if not f then
        template_cache[path] = false
        return LOGIN_HTML
    end
    local s = f:read("*a")
    f:close()
    if not s or s == "" then
        ngx.log(ngx.WARN, "totp_gate: template ", path, " empty - using baked default")
        template_cache[path] = false
        return LOGIN_HTML
    end
    if not s:find("__ACTION__", 1, true) then
        ngx.log(ngx.WARN, "totp_gate: template ", path,
                " missing __ACTION__ placeholder - using baked default")
        template_cache[path] = false
        return LOGIN_HTML
    end
    template_cache[path] = s
    return s
end

-- Per-IP failed-attempt counter. Soft-degrades if the shared dict
-- isn't declared (logs one NOTICE per worker so the gap is visible).
local function get_ratelimit_dict(dict_name)
    local shd = ngx.shared[dict_name]
    if shd then return shd end
    if not rate_limit_warned then
        ngx.log(ngx.NOTICE,
            "totp_gate: rate-limit OFF: `lua_shared_dict ", dict_name,
            " 10m;` not declared. Add it in conf.d/00-auth-gate.conf to enable",
            " brute-force protection (10^6 codes at 6 digits is small).")
        rate_limit_warned = true
    end
    return nil
end

local function ratelimit_count(shd, ip)
    if not shd then return 0 end
    return shd:get("fail:" .. ip) or 0
end

local function ratelimit_record_failure(shd, ip, window)
    if not shd then return end
    -- TTL is set on first incr only, subsequent incrs don't extend it -
    -- so hammering can't keep the lockout alive past `window`.
    shd:incr("fail:" .. ip, 1, 0, window)
end

local function ratelimit_clear(shd, ip)
    if shd then shd:delete("fail:" .. ip) end
end

local function ratelimit_remaining(shd, ip)
    if not shd then return 0 end
    local ttl = shd:ttl("fail:" .. ip)
    if not ttl or ttl < 0 then return 0 end
    return ttl
end

local function format_minutes(seconds)
    local minutes = math.ceil(seconds / 60)
    if minutes <= 1 then return "1 minute" end
    return tostring(minutes) .. " minutes"
end

local function html_escape(s)
    return (s:gsub("&", "&amp;"):gsub("<", "&lt;"):gsub(">", "&gt;")
             :gsub("\"", "&quot;"):gsub("'", "&#39;"))
end

-- err_kind / notice_kind set data-* attrs the template JS uses to
-- localise (or leave alone, for "locked" which carries a countdown).
local function serve_login(template_path, login_path, next_url,
                           err_msg, err_kind, status, notice_msg, notice_kind)
    local action = login_path
    if next_url and next_url ~= "/" then
        action = action .. "?next=" .. ngx.escape_uri(next_url)
    end
    local err_html = ""
    if err_msg then
        err_html = '<p class="err" data-msg="' .. html_escape(err_kind or "invalid") ..
                   '">' .. html_escape(err_msg) .. '</p>'
    end
    local notice_html = ""
    if notice_msg then
        notice_html = '<p class="notice" data-kind="' ..
                      html_escape(notice_kind or "info") .. '">' ..
                      html_escape(notice_msg) .. '</p>'
    end
    -- Single-pass substitution: each placeholder is replaced once,
    -- so a replacement that happens to contain `__OTHER__` literally
    -- won't get re-matched. Unknown `__XXX__` tokens are kept as-is
    -- (function returns nil → gsub keeps the match). Function form
    -- (rather than string form) because `%` in URLs is a capture ref.
    local replacements = {
        ACTION = html_escape(action),
        NOTICE = notice_html,
        ERR    = err_html,
    }
    local body = load_template(template_path):gsub("__([A-Z]+)__", function(key)
        return replacements[key]
    end)
    ngx.status = status or (err_msg and 401 or 200)
    ngx.header["Content-Type"]  = "text/html; charset=utf-8"
    ngx.header["Cache-Control"] = "no-store"
    ngx.print(body)
    return ngx.exit(ngx.status)
end

function _M.guard(opts)
    opts = opts or {}

    -- Strip client-supplied copies of the identity headers a gate in this
    -- family may forward, so no path through the gate can smuggle them.
    ngx.req.clear_header("X-Auth-Sub")
    ngx.req.clear_header("X-Auth-User")
    ngx.req.clear_header("X-Auth-Email")

    local secret_path     = opts.secret_file         or DEFAULT_SECRET_PATH
    local cookie_key_path = opts.cookie_key_file     or DEFAULT_COOKIE_KEY_PATH
    local template_path   = opts.login_template_file  -- nil = baked default
    local ttl             = opts.session_ttl         or DEFAULT_SESSION_TTL
    local login_path      = opts.login_path          or DEFAULT_LOGIN_PATH
    local max_fails       = opts.max_failures        or DEFAULT_MAX_FAILURES
    local fail_window     = opts.failure_window      or DEFAULT_FAILURE_WINDOW
    local dict_name       = opts.shared_dict_name    or DEFAULT_DICT_NAME
    -- Optional info notice (oidc_gate uses this for IdP-down fallback).
    -- notice_kind = data-kind attr the template JS keys off to localise.
    local notice          = opts.notice
    local notice_kind     = opts.notice_kind

    if ip_allowed(ngx.var.remote_addr, opts)             then return end
    if path_in_list(ngx.var.uri, opts.whitelist_paths)   then return end

    local key = load_required(cookie_key_cache, cookie_key_path, "cookie key file")

    if verify_session(key, ngx.var["cookie_" .. COOKIE_NAME]) then return end

    local method   = ngx.req.get_method()
    local next_url = safe_next(ngx.var.arg_next)
    local ip       = ngx.var.remote_addr or "unknown"
    local shd      = get_ratelimit_dict(dict_name)
    local fails    = ratelimit_count(shd, ip)

    -- Locked out → 429 + login page. Don't re-increment (would extend it).
    if fails >= max_fails then
        local remaining = ratelimit_remaining(shd, ip)
        if remaining <= 0 then remaining = fail_window end
        ngx.header["Retry-After"] = tostring(remaining)
        ngx.log(ngx.WARN, "totp_gate: locked out ", ip,
                " (", fails, " failures); ", remaining, "s remaining")
        return serve_login(template_path, login_path, next_url,
            "Too many failed attempts. Try again in " ..
                format_minutes(remaining) .. ".",
            "locked", 429, notice, notice_kind)
    end

    -- Login endpoint
    if ngx.var.uri == login_path then
        if method == "POST" then
            ngx.req.read_body()
            local args = ngx.req.get_post_args()
            local code = args and args.code
            local secret = load_required(secret_cache, secret_path, "secret file")
            if type(code) == "string" and totp.verify(secret, code) then
                ratelimit_clear(shd, ip)
                local expiry = ngx.time() + ttl
                local secure = (ngx.var.scheme == "https") and "; Secure" or ""
                ngx.header["Set-Cookie"] =
                    COOKIE_NAME .. "=" .. sign_session(key, expiry) ..
                    "; Path=/; HttpOnly; SameSite=Strict; Max-Age=" .. ttl .. secure
                return ngx.redirect(next_url, 303)
            end
            ratelimit_record_failure(shd, ip, fail_window)
            ngx.log(ngx.WARN, "totp_gate: failed code from ", ip,
                    " (failures=", fails + 1, "/", max_fails, ")")
            return serve_login(template_path, login_path, next_url,
                "Invalid code - try again.", "invalid", nil, notice, notice_kind)
        end
        return serve_login(template_path, login_path, next_url,
            nil, nil, nil, notice, notice_kind)
    end

    -- Other URLs: redirect GET/HEAD to login; reject other methods.
    if method == "GET" or method == "HEAD" then
        return ngx.redirect(
            login_path .. "?next=" .. ngx.escape_uri(ngx.var.request_uri),
            303
        )
    end
    -- Non-GET unauthenticated request (almost always XHR/fetch from
    -- a SPA). Return 419, NOT 401 - failed login attempts (POST to
    -- the login path with a wrong code) keep their 401 via
    -- serve_login() so CrowdSec brute-force scenarios still catch
    -- credential-guessing. This branch is the "you have no session
    -- at all" signal, which a SPA polling /api/* generates en masse
    -- and shouldn't ban the legitimate user with.
    ngx.status = 419
    ngx.header["Content-Type"]  = "application/json"
    ngx.header["Cache-Control"] = "no-store"
    ngx.print('{"error":"session_required"}\n')
    return ngx.exit(419)
end

function _M.has_valid_session(opts)
    opts = opts or {}
    local cookie_key_path = opts.cookie_key_file or DEFAULT_COOKIE_KEY_PATH
    local key = cookie_key_cache[cookie_key_path]
    if not key then
        local data = read_trimmed(cookie_key_path)
        if not data then return false end
        cookie_key_cache[cookie_key_path] = data
        key = data
    end
    return verify_session(key, ngx.var["cookie_" .. COOKIE_NAME])
end

return _M
