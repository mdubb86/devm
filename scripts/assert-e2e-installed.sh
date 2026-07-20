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
SOCK="$HOME/Library/Application Support/devm-e2e/devm.sock"
[ -S "$SOCK" ] || fail "daemon socket missing: $SOCK"

# 9. Fingerprint match — daemon fingerprint == /usr/local/bin/devm-e2e's
DEVM_BIN=/usr/local/bin/devm-e2e
[ -x "$DEVM_BIN" ] || fail "missing $DEVM_BIN"
command -v jq >/dev/null 2>&1 || fail "jq not installed (required for fingerprint check; brew install jq)"
"$DEVM_BIN" status --json 2>/dev/null | \
    jq -e '.daemon.running == true and .daemon.fingerprint_matches_cli == true' \
    >/dev/null 2>&1 || fail "daemon not reachable or fingerprint mismatch"

echo "assert-e2e-installed: ok"
