#!/bin/sh
# lightngx container entrypoint: seed config, prepare logs, run
# drop-in hooks, then hand over to lightngx (which supervises nginx).
set -eu

# Seed /etc/nginx from the image template when the (usually bind-mounted)
# directory is empty. An existing config is never touched.
if [ -z "$(ls -A /etc/nginx 2>/dev/null)" ]; then
    echo "[lightngx] /etc/nginx is empty, seeding from /usr/local/etc/nginx"
    cp -a /usr/local/etc/nginx/. /etc/nginx/
fi

# Seed the UI reverse-proxy config once if absent (works for an existing
# config too); never clobber a copy you have edited.
if [ ! -e /etc/nginx/conf.d/lightngx.conf ] \
   && [ -f /usr/share/lightngx/conf/lightngx.conf ]; then
    mkdir -p /etc/nginx/conf.d
    cp /usr/share/lightngx/conf/lightngx.conf /etc/nginx/conf.d/lightngx.conf
    echo "[lightngx] seeded conf.d/lightngx.conf"
fi

# Own the whole nginx config as the worker user, so the unprivileged workers can
# always read what lightngx (and you) put there. Config is 0644 and readable
# either way, but 0600 drop-ins like the auth-gate key files need the right
# owner. The user is the nginx `user` directive (LN_NGINX_USER overrides);
# LN_FIX_CONFIG_PERMS=false turns this off. The master still runs as root, so
# nginx keeps binding 80/443 (and it works under no-new-privileges).
if [ "${LN_FIX_CONFIG_PERMS:-true}" = "true" ]; then
    nuser="${LN_NGINX_USER:-$(awk '$1=="user"{gsub(/;/,"",$2); print $2; exit}' /etc/nginx/nginx.conf 2>/dev/null)}"
    nuser="${nuser:-nginx}"
    if id "$nuser" >/dev/null 2>&1; then
        echo "[lightngx] owning /etc/nginx as $nuser (the worker user)"
        chown -R "$nuser":"$nuser" /etc/nginx 2>/dev/null || true
        # Lock the auth-gate secrets to the owner (no group/other access).
        [ -d /etc/nginx/gates ] && chmod -R go-rwx /etc/nginx/gates 2>/dev/null || true
    else
        echo "[lightngx] LN_FIX_CONFIG_PERMS: user '$nuser' not found, skipping chown"
    fi
fi

# The nginx base image symlinks its logs to /dev/stdout|stderr, which the
# log viewer cannot read. Replace them with real files unless the user
# opts back into docker-style logging with LN_DOCKER_LOGS=true.
if [ "${LN_DOCKER_LOGS:-false}" != "true" ]; then
    for f in access.log error.log; do
        if [ -L "/var/log/nginx/$f" ]; then
            rm -f "/var/log/nginx/$f"
            touch "/var/log/nginx/$f"
        fi
    done
fi

# Drop-in hooks, ordered by name: the replacement for s6 oneshots when
# rebasing images that add integrations (crowdsec, vts, ...). A failing
# hook aborts startup so breakage is loud, not silent.
if [ -d /docker-entrypoint.d ]; then
    for hook in $(find /docker-entrypoint.d -maxdepth 1 -name '*.sh' -type f | sort); do
        if [ -x "$hook" ]; then
            echo "[lightngx] running hook $hook"
            "$hook"
        fi
    done
fi

exec /usr/local/bin/lightngx "$@"
