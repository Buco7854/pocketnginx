-- RFC 6238 TOTP / RFC 4226 HOTP — pure Lua + ngx.hmac_sha1 (lua-nginx-
-- module builtin). No external deps. Time-step 30s, 6 digits, SHA-1
-- (the only thing all authenticator apps universally accept).

local bit = require("bit")

local _M = { _VERSION = "0.1" }

local B32 = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
local B32_INDEX = {}
for i = 1, #B32 do B32_INDEX[B32:sub(i, i)] = i - 1 end

function _M.base32_decode(input)
    if type(input) ~= "string" then return nil end
    input = input:upper():gsub("=", ""):gsub("%s+", "")
    local bits, value, out = 0, 0, {}
    for i = 1, #input do
        local v = B32_INDEX[input:sub(i, i)]
        if not v then return nil end
        value = value * 32 + v
        bits = bits + 5
        if bits >= 8 then
            bits = bits - 8
            out[#out + 1] = string.char(math.floor(value / 2 ^ bits) % 256)
            -- Keep only the low `bits` bits — otherwise `value` grows
            -- unbounded (32 base32 chars = 160 bits → blows past double
            -- precision well before the end of a real TOTP secret).
            value = value % (2 ^ bits)
        end
    end
    return table.concat(out)
end

local function counter_to_be8(counter)
    local b = {}
    for i = 8, 1, -1 do
        b[i] = string.char(counter % 256)
        counter = math.floor(counter / 256)
    end
    return table.concat(b)
end

function _M.hotp(key, counter)
    local hmac = ngx.hmac_sha1(key, counter_to_be8(counter))
    local offset = bit.band(hmac:byte(20), 0x0f)
    local bin =
        bit.band(hmac:byte(offset + 1), 0x7f) * 16777216 +
        hmac:byte(offset + 2) * 65536 +
        hmac:byte(offset + 3) * 256 +
        hmac:byte(offset + 4)
    return string.format("%06d", bin % 1000000)
end

function _M.totp(secret_b32, t)
    local key = _M.base32_decode(secret_b32)
    if not key or key == "" then return nil end
    return _M.hotp(key, math.floor((t or ngx.time()) / 30))
end

-- Accept the previous, current, and next 30s window to tolerate up to
-- 30s of clock skew on either side. Constant-time string compare to
-- avoid timing-distinguishing valid vs invalid codes.
local function ct_eq(a, b)
    if #a ~= #b then return false end
    local diff = 0
    for i = 1, #a do
        diff = bit.bor(diff, bit.bxor(a:byte(i), b:byte(i)))
    end
    return diff == 0
end

function _M.verify(secret_b32, code)
    if type(code) ~= "string" or not code:match("^%d%d%d%d%d%d$") then
        return false
    end
    local key = _M.base32_decode(secret_b32)
    if not key or key == "" then return false end
    local now = math.floor(ngx.time() / 30)
    local ok = false
    for step = now - 1, now + 1 do
        if ct_eq(_M.hotp(key, step), code) then ok = true end
    end
    return ok
end

return _M
