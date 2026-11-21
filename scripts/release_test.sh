#!/usr/bin/env bash
# scripts/release_test.sh — bash smoke for scripts/release.sh.

set -euo pipefail

PASS=0
FAIL=0

die() { echo "FAIL: $*" >&2; FAIL=$((FAIL+1)); }
ok()  { echo "PASS: $*"; PASS=$((PASS+1)); }

# Fixtures use the real GitHub URLs, rewritten to a local bare repo. Only
# local transports are allowed, so a missed rewrite fails instead of
# reaching GitHub.
export GIT_ALLOW_PROTOCOL=file

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RELEASE_SH="$REPO_ROOT/scripts/release.sh"

# make_fixture creates a tempdir with:
#   $base/repo   — clean git repo with optional starting tag; origin is the
#                  real devm-dev URL, rewritten (insteadOf) to $base/origin.git
#                  so nothing reaches GitHub; scripts/publish.sh is a stub
#                  logging its argv to $base/publish.log
#   $base/scaffold/release.sh — copy of release.sh with git push no-opped
#   $base/scaffold/stubs/{go,just,gh} — env stubs that exit 0
# Echoing $base lets each test set things up + tear down cleanly.
make_fixture() {
    local start_tag="$1"
    local base
    base=$(mktemp -d)
    mkdir -p "$base/repo" "$base/scaffold/stubs"
    (
        cd "$base/repo"
        git init -q
        git config user.email t@t.t && git config user.name t
        git checkout -q -b main
        git init -q --bare "$base/origin.git"
        git config "url.$base/origin.git.insteadOf" git@github.com:mdubb86/devm-dev.git
        git config --add "url.$base/origin.git.insteadOf" git@github.com:mdubb86/devm.git
        git remote add origin git@github.com:mdubb86/devm-dev.git
        mkdir scripts
        # Stub publish.sh: logs every call. If PREFLIGHT_FAIL=1 is set in
        # the environment release.sh runs under, `preflight` fails so we
        # can assert release.sh refuses to tag.
        cat > scripts/publish.sh <<EOF
#!/usr/bin/env bash
echo "publish \$*" >> "$base/publish.log"
if [ "\$1" = preflight ] && [ "\${PREFLIGHT_FAIL:-}" = 1 ]; then
    echo "publish: stubbed preflight failure" >&2
    exit 1
fi
EOF
        chmod +x scripts/publish.sh
        echo x > x && git add -A && git commit -q -m "init"
        if [ -n "$start_tag" ]; then
            git tag "$start_tag"
        fi
    )
    cp "$RELEASE_SH" "$base/scaffold/release.sh"
    sed -i '' 's|^git push origin|: "would push:"|' "$base/scaffold/release.sh"
    for cmd in go just gh; do
        cat > "$base/scaffold/stubs/$cmd" <<EOF
#!/usr/bin/env bash
echo "[stub] $cmd \$*" >&2
exit 0
EOF
        chmod +x "$base/scaffold/stubs/$cmd"
    done
    echo "$base"
}

run_release() {
    local base="$1"; shift
    (
        cd "$base/repo"
        # shellcheck disable=SC2030,SC2031 # intentional: this subshell's PATH is isolated
        export PATH="$base/scaffold/stubs:$PATH"
        SKIP_E2E=1 SKIP_CI_CHECK=1 NONINTERACTIVE=1 \
            "$base/scaffold/release.sh" "$@" 2>&1
    )
}

# Test 1: patch bump from v0.31.0 → v0.31.1
BASE=$(make_fixture v0.31.0)
OUT=$(run_release "$BASE" v0.31.1 || true)
if echo "$OUT" | grep -q 'tagged v0.31.1'; then
    ok "patch bump computes v0.31.1"
else
    die "patch bump did not tag v0.31.1 (got: $OUT)"
fi
rm -rf "$BASE"

# Test 2: no tag yet → first tag accepted
BASE=$(make_fixture "")
OUT=$(run_release "$BASE" v0.0.1 || true)
if echo "$OUT" | grep -q 'tagged v0.0.1'; then
    ok "first tag accepted when no prior tag exists"
else
    die "first tag scenario failed (got: $OUT)"
fi
rm -rf "$BASE"

# Test 3: dirty tree → refuses
BASE=$(make_fixture v0.31.0)
echo dirty > "$BASE/repo/dirty.txt"
OUT=$(run_release "$BASE" v0.31.1 || true)
if echo "$OUT" | grep -q 'working tree is dirty'; then
    ok "dirty tree refused"
else
    die "dirty tree should have been refused (got: $OUT)"
fi
rm -rf "$BASE"

# Test 4: not on main → refuses
BASE=$(make_fixture v0.31.0)
( cd "$BASE/repo" && git checkout -q -b not-main )
OUT=$(run_release "$BASE" v0.31.1 || true)
if echo "$OUT" | grep -q 'must be on main'; then
    ok "non-main branch refused"
else
    die "non-main branch should have been refused (got: $OUT)"
fi
rm -rf "$BASE"

# Test 5: origin still the old public URL → refuses before tagging
BASE=$(make_fixture v0.31.0)
git -C "$BASE/repo" remote set-url origin git@github.com:mdubb86/devm.git
OUT=$(run_release "$BASE" v0.31.1 || true)
if echo "$OUT" | grep -q 'expected git@github.com:mdubb86/devm-dev.git' \
    && ! git -C "$BASE/repo" rev-parse -q --verify refs/tags/v0.31.1 >/dev/null; then
    ok "old public origin refused"
else
    die "old public origin should have been refused (got: $OUT)"
fi
rm -rf "$BASE"

# Test 6: NONINTERACTIVE release publishes with --confirm
BASE=$(make_fixture v0.31.0)
OUT=$(run_release "$BASE" v0.31.1 || true)
if grep -qx 'publish publish v0.31.1 --confirm' "$BASE/publish.log" 2>/dev/null \
    && echo "$OUT" | grep -q 'published v0.31.1 to mdubb86/devm'; then
    ok "release publishes with --confirm"
else
    die "release should publish with --confirm (got: $OUT; log: $(cat "$BASE/publish.log" 2>/dev/null))"
fi
rm -rf "$BASE"

# Test 7: preflight runs BEFORE the tag — on success, both lines land, and
# preflight precedes the publish call in the log.
BASE=$(make_fixture v0.31.0)
OUT=$(run_release "$BASE" v0.31.1 || true)
FIRST_LINE=$(head -1 "$BASE/publish.log" 2>/dev/null || true)
if [ "$FIRST_LINE" = "publish preflight" ] \
    && grep -qx 'publish publish v0.31.1 --confirm' "$BASE/publish.log"; then
    ok "preflight runs before publish in the happy path"
else
    die "preflight should run first (log: $(cat "$BASE/publish.log" 2>/dev/null))"
fi
rm -rf "$BASE"

# Test 8: preflight failure stops the release BEFORE tagging.
BASE=$(make_fixture v0.31.0)
OUT=$(
    cd "$BASE/repo"
    # shellcheck disable=SC2030,SC2031 # intentional: this subshell's PATH is isolated
    export PATH="$BASE/scaffold/stubs:$PATH"
    SKIP_E2E=1 SKIP_CI_CHECK=1 NONINTERACTIVE=1 PREFLIGHT_FAIL=1 \
        "$BASE/scaffold/release.sh" v0.31.1 2>&1 || true
)
if echo "$OUT" | grep -q 'stubbed preflight failure' \
    && ! git -C "$BASE/repo" rev-parse -q --verify refs/tags/v0.31.1 >/dev/null \
    && ! grep -qF 'publish v0.31.1' "$BASE/publish.log"; then
    ok "preflight failure stops release before tagging"
else
    die "preflight failure should stop release before tagging (got: $OUT; log: $(cat "$BASE/publish.log" 2>/dev/null))"
fi
rm -rf "$BASE"

echo
echo "results: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
