#!/usr/bin/env bash
# scripts/release_test.sh — bash smoke for scripts/release.sh.

set -euo pipefail

PASS=0
FAIL=0

die() { echo "FAIL: $*" >&2; FAIL=$((FAIL+1)); }
ok()  { echo "PASS: $*"; PASS=$((PASS+1)); }

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RELEASE_SH="$REPO_ROOT/scripts/release.sh"

# make_fixture creates a tempdir with two children:
#   $base/repo   — clean git repo with optional starting tag
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
        echo x > x && git add x && git commit -q -m "init"
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

echo
echo "results: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
