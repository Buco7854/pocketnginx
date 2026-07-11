# syntax=docker/dockerfile:1.7
#
# lightngx ships in two flavours from this one Dockerfile:
#
#   --target light  ->  nginx (official Debian image) + the single static Go
#                       binary that serves the management UI and supervises
#                       the nginx master. Nothing else — the lean default.
#
#   --target full   ->  light PLUS the CrowdSec lua bouncer, nginx-module-vts,
#                       and lua-resty-openidc. CrowdSec turns on from
#                       CROWDSEC_LAPI_KEY; the VTS module is loaded by default
#                       (inert until you add a zone). On the light image the
#                       CrowdSec env safely no-ops with a warning.
#
# The full-only modules can never come from Debian's libnginx-mod-* —
# nginx's dynamic-module ABI check is EXACT and --with-compat does not relax
# it — so NDK + lua-nginx-module + LuaJIT + VTS are built from source against
# the EXACT nginx the base ships (detected from `nginx -v`), and a build-time
# `nginx -t` loads all three modules and resolves every lua dep: upstream
# breakage fails the BUILD, never ships silently. Pin one-offs with *_REF.

ARG NGINX_IMAGE=nginx:mainline-trixie

#############################################################
# Stage 1: frontend
#############################################################
FROM node:22-trixie-slim AS web
WORKDIR /src/web/app
COPY web/app/package.json web/app/package-lock.json ./
RUN npm ci
COPY web/app/ ./
RUN npm run build

#############################################################
# Stage 2: backend (frontend embedded)
#############################################################
FROM golang:1.25-trixie AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
        -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/lightngx ./cmd/lightngx

#############################################################
# Stage 3 (assets): CrowdSec bouncer .deb + pure-lua deps   [full only]
#############################################################
FROM debian:trixie-slim AS assets

ARG CROWDSEC_GPG_FINGERPRINT=6A89E3C2303A901A889971D3376ED5326E93CD0C
# packagecloud only publishes this repo up to bookworm (no trixie suite),
# but the .deb is pure lua/config/html (asserted below) so the suite is
# irrelevant to what we extract — pin bookworm regardless of the base.
ARG CROWDSEC_DEBIAN_RELEASE=bookworm
ARG CS_NGINX_BOUNCER_VERSION=

RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends curl ca-certificates gnupg luarocks; \
    \
    curl -fsSL https://packagecloud.io/crowdsec/crowdsec/gpgkey -o /tmp/cs.key; \
    fpr="$(gpg --show-keys --with-fingerprint --with-colons /tmp/cs.key \
            | awk -F: '/^fpr/ {print $10; exit}')"; \
    [ "$fpr" = "${CROWDSEC_GPG_FINGERPRINT}" ] \
        || { echo "FATAL: CrowdSec GPG fingerprint mismatch ($fpr)" >&2; exit 1; }; \
    install -d /usr/share/keyrings; \
    gpg --dearmor < /tmp/cs.key > /usr/share/keyrings/crowdsec.gpg; \
    echo "deb [signed-by=/usr/share/keyrings/crowdsec.gpg] https://packagecloud.io/crowdsec/crowdsec/debian/ ${CROWDSEC_DEBIAN_RELEASE} main" \
        > /etc/apt/sources.list.d/crowdsec.list; \
    apt-get update; \
    \
    mkdir -p /assets/deb; cd /assets/deb; \
    if [ -n "${CS_NGINX_BOUNCER_VERSION}" ]; then \
        apt-get download "crowdsec-nginx-bouncer=${CS_NGINX_BOUNCER_VERSION}*"; \
    else \
        apt-get download crowdsec-nginx-bouncer; \
    fi; \
    mkdir -p /assets/bouncer; \
    dpkg-deb -x "$(ls crowdsec-nginx-bouncer_*.deb)" /assets/bouncer; \
    \
    # lua-resty-http / -string: tiny pure-lua bouncer deps not in Debian.
    luarocks install lua-resty-http   --tree=/assets/luatree; \
    luarocks install lua-resty-string --tree=/assets/luatree; \
    # lua-resty-openidc (+ its pure-lua dep tree: -session, -jwt, -jit-uuid,
    # -hmac, -rsa — all LuaJIT FFI over the base OpenSSL, nothing in
    # lib/lua/5.1). The build guard requires resty.openidc.
    luarocks install lua-resty-openidc --tree=/assets/luatree; \
    # pure-lua => no lib/lua/5.1; pre-create so the final COPY is a no-op.
    mkdir -p /assets/luatree/lib/lua/5.1; \
    \
    # Guard: the .deb must stay pure lua/config (no ELF), and ship the
    # files we depend on.
    if find /assets/bouncer -type f -exec sh -c 'head -c4 "$1"|grep -q ELF' _ {} \; -print | grep -q .; then \
        echo "FATAL: CrowdSec bouncer .deb now ships native binaries" >&2; exit 1; \
    fi; \
    for f in crowdsec.lua ban.html captcha.html crowdsec_nginx.conf crowdsec-nginx-bouncer.conf; do \
        find /assets/bouncer -name "$f" -print -quit | grep -q . \
            || { echo "FATAL: $f missing from CrowdSec bouncer .deb" >&2; exit 1; }; \
    done

#############################################################
# Stage 4 (builder): compile LuaJIT + NDK + lua + VTS       [full only]
#                    against the EXACT nginx the base ships
#############################################################
FROM ${NGINX_IMAGE} AS builder

# Unpinned by default: track each project's default branch (upstream
# fixes nginx-compat there first; the lua set moves in lockstep). The
# final `nginx -t` guard fails the build on an incompatible combo.
ARG VTS_REF=
ARG NDK_REF=
ARG LUA_NGINX_MODULE_REF=
ARG LUA_RESTY_CORE_REF=
ARG LUA_RESTY_LRUCACHE_REF=
ARG LUAJIT2_REF=

# VTS dashboard baked into the module. By default this is our Lightngx-styled
# dashboard (docker/vts-dashboard.html, tracked). Override it by dropping your
# own status.html into docker/vts/ before building, or set
# VTS_STOCK_DASHBOARD=1 to fall back to nginx-module-vts's stock page.
ARG VTS_STOCK_DASHBOARD=
COPY docker/vts-dashboard.html /tmp/vts-default.html
COPY docker/vts/               /tmp/vts-custom/

RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        ca-certificates curl git build-essential perl libpcre2-dev libssl-dev zlib1g-dev; \
    NGX_VER="$(nginx -v 2>&1 | grep -oE '[0-9]+\.[0-9]+\.[0-9]+')"; \
    clone() { \
        if [ -n "$3" ]; then git clone --depth 1 --branch "$3" "$2" "$1"; \
        else git clone --depth 1 "$2" "$1"; fi; \
    }; \
    cd /tmp; \
    curl -fsSL "https://nginx.org/download/nginx-${NGX_VER}.tar.gz" | tar xz; \
    \
    # LuaJIT (OpenResty fork) — lua-nginx-module's runtime, installed to
    # the exact /usr/local prefix the module links against.
    clone /tmp/luajit2 https://github.com/openresty/luajit2.git "${LUAJIT2_REF}"; \
    make -C /tmp/luajit2 -j"$(nproc)"; \
    make -C /tmp/luajit2 install PREFIX=/usr/local; \
    ldconfig; \
    export LUAJIT_LIB=/usr/local/lib; \
    export LUAJIT_INC=/usr/local/include/luajit-2.1; \
    \
    clone /tmp/ndk              https://github.com/vision5/ngx_devel_kit.git      "${NDK_REF}"; \
    clone /tmp/lua-nginx-module https://github.com/openresty/lua-nginx-module.git "${LUA_NGINX_MODULE_REF}"; \
    clone /tmp/vts              https://github.com/vozlt/nginx-module-vts.git     "${VTS_REF}"; \
    echo "compiling against nginx ${NGX_VER}:" \
         "luajit2($(git -C /tmp/luajit2 rev-parse --short HEAD))" \
         "ndk($(git -C /tmp/ndk rev-parse --short HEAD))" \
         "lua-nginx-module($(git -C /tmp/lua-nginx-module rev-parse --short HEAD))" \
         "vts($(git -C /tmp/vts rev-parse --short HEAD))"; \
    \
    # Pick the dashboard to compile in: an override in docker/vts/ wins,
    # else our tracked default, else (VTS_STOCK_DASHBOARD=1) the stock page.
    # tplToDefine.sh must run from util/ (it calls `perl fileToHex.pl` by
    # bare name) and exits 0 even on failure, so assert the macro has a body.
    DASH=/tmp/vts-default.html; \
    [ -f /tmp/vts-custom/status.html ] && DASH=/tmp/vts-custom/status.html; \
    [ -n "${VTS_STOCK_DASHBOARD}" ] && DASH=; \
    if [ -n "$DASH" ]; then \
        chmod +x /tmp/vts/util/tplToDefine.sh; \
        ( cd /tmp/vts/util && ./tplToDefine.sh "$DASH" ) \
            > /tmp/vts/src/ngx_http_vhost_traffic_status_module_html.h; \
        grep -qF '\x' /tmp/vts/src/ngx_http_vhost_traffic_status_module_html.h \
            || { echo "FATAL: VTS dashboard header is empty (tplToDefine.sh failed)" >&2; exit 1; }; \
        echo "baked VTS dashboard from $DASH"; \
    fi; \
    \
    # NDK must precede lua-nginx-module. --with-http_ssl_module is
    # REQUIRED: lua-nginx-module gates lua_ssl_* + TLS cosockets behind
    # NGX_HTTP_SSL at COMPILE time and --with-compat does not define it —
    # without it nginx rejects the CrowdSec snippet at runtime.
    cd "nginx-${NGX_VER}"; \
    ./configure --with-compat --with-http_ssl_module \
        --add-dynamic-module=/tmp/ndk \
        --add-dynamic-module=/tmp/lua-nginx-module \
        --add-dynamic-module=/tmp/vts; \
    make -j"$(nproc)" modules; \
    \
    mkdir -p /assets/modules /assets/luajit/share /assets/luaresty; \
    cp objs/ndk_http_module.so \
       objs/ngx_http_lua_module.so \
       objs/ngx_http_vhost_traffic_status_module.so /assets/modules/; \
    cp -a /usr/local/lib/libluajit-5.1.so* /assets/luajit/; \
    cp -a /usr/local/share/luajit-2.1/.    /assets/luajit/share/; \
    \
    # lua-resty-core / -lrucache: MANDATORY for modern lua-nginx-module
    # (init_by_lua loads resty.core or aborts). Same upstream branch as
    # the module => guaranteed-compatible pair.
    clone /tmp/lua-resty-core     https://github.com/openresty/lua-resty-core.git     "${LUA_RESTY_CORE_REF}"; \
    clone /tmp/lua-resty-lrucache https://github.com/openresty/lua-resty-lrucache.git "${LUA_RESTY_LRUCACHE_REF}"; \
    cp -a /tmp/lua-resty-core/lib/.     /assets/luaresty/; \
    cp -a /tmp/lua-resty-lrucache/lib/. /assets/luaresty/

#############################################################
# Target: light — nginx + the lightngx binary, nothing else
#############################################################
FROM ${NGINX_IMAGE} AS light

RUN set -eux; grep -qiE '^ID(_LIKE)?=.*debian' /etc/os-release

# Runtime deps: ca-certificates (OIDC login to the UI's IdP over TLS) and
# logrotate (the supervisor runs it on a timer — no cron in the image).
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates logrotate; \
    rm -rf /var/lib/apt/lists/*

# Example UI reverse-proxy vhosts to copy into conf.d.
COPY docker/ui-proxy.conf     /usr/share/lightngx/examples/ui-proxy.conf
COPY docker/ui-proxy-tls.conf /usr/share/lightngx/examples/ui-proxy-tls.conf
COPY docker/logrotate-nginx.conf /etc/logrotate.d/nginx

# Debian-convention vhost layout so the sites/maintenance features work
# out of the box, then capture the pristine config as the seed template
# (used when /etc/nginx is bind-mounted empty).
RUN set -eux; \
    mkdir -p /etc/nginx/sites-available /etc/nginx/sites-enabled /etc/nginx/modules-enabled; \
    if [ -f /etc/nginx/conf.d/default.conf ]; then \
        mv /etc/nginx/conf.d/default.conf /etc/nginx/sites-available/default.conf; \
        ln -s ../sites-available/default.conf /etc/nginx/sites-enabled/default.conf; \
    fi; \
    grep -q 'sites-enabled' /etc/nginx/nginx.conf || \
        sed -i 's|include /etc/nginx/conf.d/\*.conf;|include /etc/nginx/conf.d/*.conf;\n    include /etc/nginx/sites-enabled/*;|' /etc/nginx/nginx.conf; \
    grep -q 'sites-enabled' /etc/nginx/nginx.conf; \
    mkdir -p /etc/nginx/streams-available /etc/nginx/streams-enabled; \
    grep -q 'streams-enabled' /etc/nginx/nginx.conf || \
        printf '\nstream {\n    include /etc/nginx/streams-enabled/*;\n}\n' >> /etc/nginx/nginx.conf; \
    grep -q 'streams-enabled' /etc/nginx/nginx.conf; \
    mkdir -p /usr/local/etc/nginx; \
    cp -a /etc/nginx/. /usr/local/etc/nginx/; \
    mkdir -p /var/lib/lightngx /docker-entrypoint.d /usr/share/lightngx/conf

COPY --from=build /out/lightngx /usr/local/bin/lightngx
COPY docker/entrypoint.sh    /usr/local/bin/lightngx-entrypoint
COPY docker/integrations.sh  /docker-entrypoint.d/10-integrations.sh
RUN chmod 755 /usr/local/bin/lightngx-entrypoint /usr/local/bin/lightngx \
              /docker-entrypoint.d/10-integrations.sh

ENV LN_SUPERVISE=true

EXPOSE 80 443 9000

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
    CMD ["/usr/local/bin/lightngx", "health"]

ENTRYPOINT ["/usr/local/bin/lightngx-entrypoint"]

#############################################################
# Target: full — light + CrowdSec bouncer, VTS, lua-resty-openidc
#############################################################
FROM light AS full

# Downstream module install relies on the nginx.org modules-path; fail the
# build if the base ever changes it.
RUN set -eux; \
    MODPATH="$(nginx -V 2>&1 | grep -oE -- '--modules-path=[^ ]+' | sed 's/.*=//')"; \
    [ "${MODPATH:-/usr/lib/nginx/modules}" = "/usr/lib/nginx/modules" ]

# Runtime dep: lua-cjson (the CrowdSec bouncer needs it).
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends lua-cjson; \
    rm -rf /var/lib/apt/lists/*

# Compiled modules + LuaJIT runtime. Debian's lua-cjson lands on the
# multiarch dir, off LuaJIT's default cpath — symlink it so
# `require "cjson"` resolves with no extra config.
COPY --from=builder /assets/modules /tmp/mods
COPY --from=builder /assets/luajit  /tmp/luajit
RUN set -eux; \
    MODPATH=/usr/lib/nginx/modules; \
    mkdir -p "$MODPATH"; \
    install -m644 /tmp/mods/ndk_http_module.so                      "$MODPATH/"; \
    install -m644 /tmp/mods/ngx_http_lua_module.so                  "$MODPATH/"; \
    install -m644 /tmp/mods/ngx_http_vhost_traffic_status_module.so "$MODPATH/"; \
    install -d /usr/local/lib; \
    cp -a /tmp/luajit/libluajit-5.1.so* /usr/local/lib/; \
    mkdir -p /usr/local/share/luajit-2.1; \
    cp -a /tmp/luajit/share/. /usr/local/share/luajit-2.1/; \
    ldconfig; \
    CJSON="$(find /usr/lib /usr/local/lib -name cjson.so -path '*lua/5.1/*' -print -quit)"; \
    [ -n "$CJSON" ] || { echo "FATAL: lua-cjson cjson.so not found" >&2; exit 1; }; \
    install -d /usr/local/lib/lua/5.1; \
    ln -sf "$CJSON" /usr/local/lib/lua/5.1/cjson.so; \
    rm -rf /tmp/mods /tmp/luajit

# Vendored pure-lua deps onto LuaJIT's default search path (the CrowdSec
# snippet's lua_package_path ends with `;;`): resty.core/-lrucache
# (mandatory for the module), resty.http/-string (bouncer), and the
# lua-resty-openidc dep tree.
COPY --from=builder /assets/luaresty/              /usr/local/share/lua/5.1/
COPY --from=assets  /assets/luatree/share/lua/5.1/ /usr/local/share/lua/5.1/
COPY --from=assets  /assets/luatree/lib/lua/5.1/   /usr/local/lib/lua/5.1/

# CrowdSec bouncer at the baremetal `apt install` paths: lua at
# /usr/lib/crowdsec/lua, ban/captcha templates at /var/lib/crowdsec/lua/
# templates (with pristine copies at /usr/share/crowdsec/lua-templates.dist
# so an empty bind mount can be re-seeded), bouncer config at
# /etc/crowdsec/bouncers/. The nginx snippet goes to the integrations dist
# dir — only seeded into conf.d when CROWDSEC_LAPI_KEY is set.
COPY --from=assets /assets/bouncer /tmp/bouncer
RUN set -eux; \
    mkdir -p /usr/lib/crowdsec/lua /var/lib/crowdsec/lua/templates \
             /usr/share/crowdsec/lua-templates.dist \
             /etc/crowdsec/bouncers /usr/share/lightngx/conf; \
    cp -r "$(dirname "$(find /tmp/bouncer -name crowdsec.lua -print -quit)")/." \
          /usr/lib/crowdsec/lua/; \
    tpl="$(dirname "$(find /tmp/bouncer -name ban.html -print -quit)")"; \
    cp -r "$tpl/." /var/lib/crowdsec/lua/templates/; \
    cp -r "$tpl/." /usr/share/crowdsec/lua-templates.dist/; \
    cp "$(find /tmp/bouncer -name crowdsec_nginx.conf -print -quit)" \
       /usr/share/lightngx/conf/crowdsec_nginx.conf; \
    # The snippet ships lua_ssl_trusted_certificate but no
    # lua_ssl_verify_depth, so the lua-nginx default of 1 applies to all
    # lua cosocket TLS. Depth 1 permits one intermediate; modern Let's
    # Encrypt chains carry two, so cold discovery to a public-cert LAPI
    # fails with OpenSSL error 20 even though the root IS trusted. Bump to
    # 5 — verification itself stays on. Fail the build if the anchor ever
    # disappears (a silent miss would reship the bug).
    sed -i '/lua_ssl_trusted_certificate/a lua_ssl_verify_depth 5;' \
        /usr/share/lightngx/conf/crowdsec_nginx.conf; \
    grep -q lua_ssl_verify_depth /usr/share/lightngx/conf/crowdsec_nginx.conf; \
    cp "$(find /tmp/bouncer -name crowdsec-nginx-bouncer.conf -print -quit)" \
       /etc/crowdsec/bouncers/crowdsec-nginx-bouncer.conf; \
    rm -rf /tmp/bouncer

# Integration drop-ins (dist copies, seeded on demand) + load_module files,
# image-owned outside /etc/nginx so bind mounts can't hide them.
COPY docker/conf/              /usr/share/lightngx/conf/
COPY docker/modules-available/ /usr/share/nginx/modules-available/

# Build guard: load all three modules, open a VTS zone, and run an
# init_by_lua that requires the bouncer's deps (forcing resty.core) and
# resty.openidc. One `nginx -t` validates the exact nginx<->module match,
# LuaJIT, the lua path, lua ssl, and the dep chain on the EXACT nginx this
# image ships.
RUN set -eux; \
    MODPATH=/usr/lib/nginx/modules; \
    { \
      echo "load_module $MODPATH/ndk_http_module.so;"; \
      echo "load_module $MODPATH/ngx_http_lua_module.so;"; \
      echo "load_module $MODPATH/ngx_http_vhost_traffic_status_module.so;"; \
      echo "events{}"; \
      echo "http{ vhost_traffic_status_zone; lua_shared_dict d 1m;"; \
      echo "      lua_ssl_trusted_certificate /etc/ssl/certs/ca-certificates.crt;"; \
      echo "      init_by_lua_block { require 'resty.http'; require 'resty.string';"; \
      echo "                          require 'cjson';     require 'resty.openidc' } }"; \
    } > /tmp/probe.conf; \
    nginx -t -c /tmp/probe.conf; \
    rm /tmp/probe.conf; \
    echo "modules load + lua deps resolve — OK"

EXPOSE 9113
