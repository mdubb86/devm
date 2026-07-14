#!/bin/sh
set -e
ln -sfn /opt/devm/scripts/with-devm-env /usr/local/bin/with-devm-env
chmod 0755 /opt/devm/scripts/*.sh

# --- CA install: trust devm's CA so guest processes accept iron-proxy's re-signed certs. ---
if [ -f /opt/devm/ca/devm.crt ] && ! cmp -s /opt/devm/ca/devm.crt /usr/local/share/ca-certificates/devm.crt; then
    install -o root -g root -m 0644 \
        /opt/devm/ca/devm.crt \
        /usr/local/share/ca-certificates/devm.crt
    update-ca-certificates --fresh > /dev/null
    grep -F -q -f /usr/local/share/ca-certificates/devm.crt \
        /etc/ssl/certs/ca-certificates.crt || {
        echo "FAIL: devm CA installed to CApath but not merged into ca-certificates.crt bundle" >&2
        exit 1
    }
fi

# --- Caddyfile: reverse-proxy config for hostname-declared services. ---
if [ -f /opt/devm/caddy/Caddyfile ] && ! cmp -s /opt/devm/caddy/Caddyfile /etc/caddy/Caddyfile; then
    install -o root -g root -m 0644 \
        /opt/devm/caddy/Caddyfile \
        /etc/caddy/Caddyfile
fi
