#!/bin/sh
# Wires up the CrowdSec bouncer baked into the -full image. There is no
# on/off toggle: it turns on when CROWDSEC_LAPI_KEY is set (the bouncer is
# useless without LAPI creds), linking the NDK + lua modules, seeding the
# bouncer snippet + resolver drop-in + ban/captcha templates, and writing
# CROWDSEC_LAPI_URL/KEY into the bouncer conf. On the light image (no
# bouncer) the input is accepted but warns + no-ops, so a misapplied env
# can never break nginx.
#
# The VTS module is also baked into -full and this script loads it by
# default (no activation env). It stays inert until you add a
# vhost_traffic_status zone + display server. Seeding only ever creates
# what is missing and never clobbers a file you edited.
set -eu

MA=/usr/share/nginx/modules-available
ME=/etc/nginx/modules-enabled
DIST=/usr/share/lightngx/conf
CD=/etc/nginx/conf.d

ensure_modules_include() {
    grep -q 'modules-enabled' /etc/nginx/nginx.conf || {
        sed -i '1i include /etc/nginx/modules-enabled/*.conf;' /etc/nginx/nginx.conf
        echo "[lightngx] added modules-enabled include to nginx.conf"
    }
    grep -q 'modules-enabled' /etc/nginx/nginx.conf
}

link_module() { # link-name available-name
    # Recreate when missing, dangling, or pointing elsewhere (e.g. a stale
    # link left by a previous/other image) — ln -sfn is idempotent.
    if [ "$(readlink "$ME/$1" 2>/dev/null)" != "$MA/$2" ]; then
        ln -sfn "$MA/$2" "$ME/$1"
        echo "[lightngx] enabled module $2"
    fi
}

unlink_module() { # link-name available-name — only our own symlink
    if [ -L "$ME/$1" ] && [ "$(readlink "$ME/$1")" = "$MA/$2" ]; then
        rm "$ME/$1"
        echo "[lightngx] disabled module $2"
    fi
}

seed_conf() { # file
    if [ ! -e "$CD/$1" ]; then
        cp -p "$DIST/$1" "$CD/$1"
        echo "[lightngx] seeded conf.d/$1"
    fi
}

# Returns 0 when the file is gone afterwards, 1 when a modified copy stays.
unseed_conf() { # file
    [ -e "$CD/$1" ] || return 0
    if cmp -s "$CD/$1" "$DIST/$1"; then
        rm "$CD/$1"
        echo "[lightngx] removed conf.d/$1"
        return 0
    fi
    echo "[lightngx] WARNING: conf.d/$1 was modified, leaving it in place"
    return 1
}

mkdir -p "$ME" "$CD"

# Prune broken module symlinks before nginx sees them: a dangling entry in
# modules-enabled (commonly left behind by a previous or different image)
# makes the `include modules-enabled/*.conf` glob fail with [emerg] and
# nginx won't start. A dangling symlink is always broken, so removing it is
# safe.
for l in "$ME"/*; do
    if [ -L "$l" ] && [ ! -e "$l" ]; then
        rm -f "$l"
        echo "[lightngx] removed stale module link $(basename "$l")"
    fi
done

# The compiled modules + bouncer assets exist only in the -full image;
# probe via the image-owned module-available configs the light target
# does not ship.
have_crowdsec() { [ -e "$MA/mod-http-lua.conf" ] && [ -e "$DIST/crowdsec_nginx.conf" ]; }
have_vts()      { [ -e "$MA/mod-http-vhost-traffic-status.conf" ]; }
have_lua()      { [ -e "$MA/mod-http-lua.conf" ] && [ -e "$MA/mod-http-ndk.conf" ]; }

# ---- CrowdSec bouncer ----
# The LAPI key drives activation: seeding the snippet + resolver and writing
# creds. But the ndk+lua modules are linked whenever a crowdsec_nginx.conf
# is present in conf.d (ours OR a leftover from another image) — its lua
# directives can't parse without them, and a missing module is an [emerg]
# that stops nginx from starting. Module linking therefore follows the
# snippet's presence, not the key.

if [ -n "${CROWDSEC_LAPI_KEY:-}" ] && ! have_crowdsec; then
    echo "[lightngx] WARNING: CROWDSEC_LAPI_KEY is set but this image has no CrowdSec bouncer; use the -full image. Ignoring."
fi

if have_crowdsec; then
    if [ -n "${CROWDSEC_LAPI_KEY:-}" ]; then
        seed_conf crowdsec_nginx.conf
        seed_conf resolver.conf

        # Ban/captcha templates: pristine copies are baked outside any bind
        # mount so an empty mounted templates dir can be (re)seeded.
        mkdir -p /var/lib/crowdsec/lua/templates
        for s in /usr/share/crowdsec/lua-templates.dist/*; do
            [ -e "$s" ] || continue
            d="/var/lib/crowdsec/lua/templates/$(basename "$s")"
            [ -e "$d" ] || cp -rp "$s" "$d"
        done

        # Write the LAPI creds into the bouncer conf. Rewrite in place
        # (truncate + write): the conf is commonly bind-mounted as a single
        # file, which can't be renamed over (mv -> EBUSY).
        BC=/etc/crowdsec/bouncers/crowdsec-nginx-bouncer.conf
        set_bouncer_conf() { # key value
            tmp="$(mktemp)"
            awk -v k="$1" -v v="$2" '$0 ~ ("^" k "=") {print k "=" v; next} {print}' "$BC" > "$tmp"
            cat "$tmp" > "$BC"
            rm -f "$tmp"
        }
        [ -n "${CROWDSEC_LAPI_URL:-}" ] && set_bouncer_conf API_URL "$CROWDSEC_LAPI_URL"
        set_bouncer_conf API_KEY "$CROWDSEC_LAPI_KEY"
    else
        # No creds: drop our own seeded snippet if it is unmodified. A
        # foreign/edited one is left in place (see the linking below).
        unseed_conf crowdsec_nginx.conf || true
        unseed_conf resolver.conf || true
    fi

    # ndk+lua are linked unconditionally further down (the -full image always
    # loads lua), so there is nothing to link here. Just flag a present
    # snippet with no key: the bouncer stays inert until CROWDSEC_LAPI_KEY.
    if [ -e "$CD/crowdsec_nginx.conf" ] && [ -z "${CROWDSEC_LAPI_KEY:-}" ]; then
        echo "[lightngx] NOTE: conf.d/crowdsec_nginx.conf is present without CROWDSEC_LAPI_KEY; the lua modules load but the bouncer has no LAPI creds."
    fi
fi

# ---- lua + NDK: loaded on the -full image by default ----
# lua-nginx-module (with NDK) powers the CrowdSec bouncer AND any
# rewrite_by_lua auth gate you add in front of a vhost. Like VTS it loads
# unconditionally on -full and stays inert until a lua directive uses it, so
# an auth gate works with no CrowdSec and no extra module wiring. NDK must
# load before lua (10 < 50).
if have_lua; then
    ensure_modules_include
    link_module 10-mod-http-ndk.conf mod-http-ndk.conf
    link_module 50-mod-http-lua.conf mod-http-lua.conf
fi

# ---- VTS: load the compiled module on the -full image ----
# No activation env (unlike CrowdSec): -full loads the VTS module by
# default. It stays inert until you add `vhost_traffic_status_zone` + a
# `vhost_traffic_status_display` server in your own config — no zone, no
# dashboard, no vhost is created here. If your config already carries its
# own load_module for VTS, remove it: a duplicate load fails nginx -t.
if have_vts; then
    ensure_modules_include
    link_module 90-mod-http-vhost-traffic-status.conf mod-http-vhost-traffic-status.conf
fi
