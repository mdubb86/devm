#!/usr/bin/env bash
# Assert every component of the parallel e2e install is gone.
set -uo pipefail

fail() { echo "assert-e2e-uninstalled: $1" >&2; exit 1; }

# 1. Plists absent
[ ! -f /Library/LaunchDaemons/com.devm.e2e.service.plist ] || \
    fail "plist still present: com.devm.e2e.service.plist"
[ ! -f /Library/LaunchDaemons/com.devm.e2e.helper.plist ]  || \
    fail "plist still present: com.devm.e2e.helper.plist"

# 2. Plists not loaded
launchctl print system/com.devm.e2e.service >/dev/null 2>&1 && \
    fail "com.devm.e2e.service still loaded"
launchctl print system/com.devm.e2e.helper  >/dev/null 2>&1 && \
    fail "com.devm.e2e.helper still loaded"

# 3. Resolver file gone
[ ! -f /etc/resolver/e2e.test ] || \
    fail "/etc/resolver/e2e.test still present"

# 4. lo0 aliases removed
for n in $(seq 21 40); do
    ifconfig lo0 | grep -q "inet 127.42.0.$n " && \
        fail "lo0 alias 127.42.0.$n still present"
done

# 5. Group removed
dscl . -read /Groups/_devm-e2e >/dev/null 2>&1 && \
    fail "group _devm-e2e still present"

# 6. CA cert removed
security find-certificate -c 'devm-e2e Local CA' \
    /Library/Keychains/System.keychain >/dev/null 2>&1 && \
    fail "devm-e2e Local CA still in keychain"

# 7. Base image removed
tart list 2>/dev/null | awk 'NR>1 {print $2}' | grep -qx devm-e2e-base && \
    fail "devm-e2e-base tart image still present"

# 8. Runtime dir gone
[ ! -d "$HOME/Library/Application Support/devm-e2e" ] || \
    fail "runtime dir still present: ~/Library/Application Support/devm-e2e"

# 9. Helper binary + socket gone
[ ! -f /usr/local/bin/devm-e2e ]        || fail "binary still present: /usr/local/bin/devm-e2e"
[ ! -f /usr/local/bin/devm-e2e-helper ] || fail "binary still present: /usr/local/bin/devm-e2e-helper"
[ ! -S /var/run/devm-e2e-helper.sock ]  || fail "helper socket still present"

# 10. ~/.ssh/config Include line removed
INCLUDE_LINE="Include \"$HOME/Library/Application Support/devm-e2e/ssh_config\""
if grep -qF "$INCLUDE_LINE" "$HOME/.ssh/config" 2>/dev/null; then
    fail "Include line still present in ~/.ssh/config: $INCLUDE_LINE"
fi

echo "assert-e2e-uninstalled: ok"
