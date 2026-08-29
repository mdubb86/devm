#!/usr/bin/env bash
# Assert every component of the parallel e2e install is present.
# Used by `just e2e-bootstrap` (post-install check) and `just e2e`
# (preflight before running tests).
set -uo pipefail

fail() { echo "assert-e2e-installed: $1" >&2; exit 1; }

# 1. Plists exist
[ -f /Library/LaunchDaemons/com.devm.e2e.service.plist ] || \
    fail "missing plist: com.devm.e2e.service.plist"
[ -f /Library/LaunchDaemons/com.devm.e2e.helper.plist ]  || \
    fail "missing plist: com.devm.e2e.helper.plist"

# 2. Plists loaded
launchctl print system/com.devm.e2e.service >/dev/null 2>&1 || \
    fail "com.devm.e2e.service not loaded (launchctl print failed)"
launchctl print system/com.devm.e2e.helper  >/dev/null 2>&1 || \
    fail "com.devm.e2e.helper not loaded"

# 3. Resolver file
[ -f /etc/resolver/e2e.test ] || fail "missing /etc/resolver/e2e.test"
grep -q "^port 51154$" /etc/resolver/e2e.test || \
    fail "/etc/resolver/e2e.test has wrong port"

# 4. lo0 aliases (127.42.0.21..40)
for n in $(seq 21 40); do
    ifconfig lo0 | grep -q "inet 127.42.0.$n " || \
        fail "missing lo0 alias 127.42.0.$n"
done

# 5. Group
dscl . -read /Groups/_devm-e2e >/dev/null 2>&1 || \
    fail "missing group _devm-e2e"

# 6. CA cert
security find-certificate -c 'devm-e2e Local CA' \
    /Library/Keychains/System.keychain >/dev/null 2>&1 || \
    fail "devm-e2e Local CA not in system keychain"

# 7. Base image
tart list 2>/dev/null | awk 'NR>1 {print $2}' | grep -qx devm-e2e-base || \
    fail "missing tart image devm-e2e-base"

# 8. Daemon UDS reachable
#
# launchd may take up to a couple of seconds after `devm install` returns
# before it starts com.devm.e2e.service and the daemon binds its socket.
# Poll for the socket for up to 10s so the bootstrap step doesn't fail
# just because we checked one moment too early.
SOCK="$HOME/Library/Application Support/devm-e2e/devm.sock"
for _ in $(seq 1 20); do
    [ -S "$SOCK" ] && break
    sleep 0.5
done
[ -S "$SOCK" ] || fail "daemon socket missing after 10s: $SOCK"

# 9. Fingerprint match — daemon fingerprint == /usr/local/bin/devm-e2e's.
# Same startup race: the daemon may accept a `status --json` call and
# report itself running once the socket appears, but poll a few seconds
# in case fingerprint publication trails socket bind.
DEVM_BIN=/usr/local/bin/devm-e2e
[ -x "$DEVM_BIN" ] || fail "missing $DEVM_BIN"
command -v jq >/dev/null 2>&1 || fail "jq not installed (required for fingerprint check; brew install jq)"
for _ in $(seq 1 20); do
    "$DEVM_BIN" status --json 2>/dev/null | \
        jq -e '.daemon.running == true and .daemon.fingerprint_matches_cli == true' \
        >/dev/null 2>&1 && ok=1 && break
    sleep 0.5
done
[ "${ok:-0}" = 1 ] || fail "daemon not reachable or fingerprint mismatch after 10s"

# 10. ~/.ssh/config Include line
INCLUDE_LINE="Include \"$HOME/Library/Application Support/devm-e2e/ssh_config\""
grep -qF "$INCLUDE_LINE" "$HOME/.ssh/config" 2>/dev/null || \
    fail "missing Include line in ~/.ssh/config: $INCLUDE_LINE"

# 11. Embed-profile pin (v0.9.3): the extracted helper binary must carry
# the identity.Profile=e2e ldflag. A wrong-profile helper (Profile=prod
# embedded in devm-e2e — e.g. because just build-e2e didn't chain
# _build-helper-embed "e2e") silently binds prod's socket path and
# collides with a real prod install.
HELPER_STRINGS="$(strings /usr/local/bin/devm-e2e-helper 2>/dev/null)"
if ! grep -q 'identity.Profile=e2e' <<<"$HELPER_STRINGS"; then
    fail "/usr/local/bin/devm-e2e-helper does not carry identity.Profile=e2e ldflag — was the embed blob rebuilt with e2e profile?"
fi

echo "assert-e2e-installed: ok"
