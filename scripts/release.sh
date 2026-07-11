#!/usr/bin/env bash
# scripts/release.sh — pre-tag picker + guards for `just release`.
#
# Env knobs (all optional):
#   SKIP_CI_CHECK=1   skip the gh CI-green check
#   NONINTERACTIVE=1  no picker; reads VERSION from $1
#
# The e2e suite is NOT run as a release guard — it needs sudo/Touch ID
# (installs the LaunchDaemon) and can't run under a scripted shell.
# Run `just e2e-devm` manually before tagging.

set -euo pipefail

die() { echo "release: $*" >&2; exit 1; }
log() { echo "release: $*" >&2; }

current_tag() {
    git tag --list 'v*' --sort=-version:refname | head -1
}

bump() {
    local cur="${1#v}"
    local kind="$2"
    local major minor patch
    IFS=. read -r major minor patch <<<"$cur"
    major=${major:-0}; minor=${minor:-0}; patch=${patch:-0}
    case "$kind" in
        patch) patch=$((patch+1));;
        minor) minor=$((minor+1)); patch=0;;
        major) major=$((major+1)); minor=0; patch=0;;
        *) die "unknown bump kind: $kind";;
    esac
    echo "v${major}.${minor}.${patch}"
}

# ---- pre-flight environment ----
[ "$(git rev-parse --abbrev-ref HEAD)" = "main" ] \
    || die "must be on main branch (currently: $(git rev-parse --abbrev-ref HEAD))"

[ -z "$(git status --porcelain)" ] \
    || die "working tree is dirty — commit or stash first"

git fetch origin --tags --quiet 2>/dev/null || true

# ---- compute candidates ----
CUR="$(current_tag)"
[ -n "$CUR" ] || CUR="v0.0.0"
PATCH="$(bump "$CUR" patch)"
MINOR="$(bump "$CUR" minor)"
MAJOR="$(bump "$CUR" major)"

# ---- pick version ----
if [ "${NONINTERACTIVE:-}" = "1" ]; then
    NEW="${1:-}"
    [ -n "$NEW" ] || die "NONINTERACTIVE=1 requires an explicit version arg"
else
    echo
    echo "  current:  $CUR"
    echo "    patch:  $PATCH   (default)"
    echo "    minor:  $MINOR"
    echo "    major:  $MAJOR"
    echo
    read -r -p "Choose [patch/minor/major]: " choice
    choice=${choice:-patch}
    case "$choice" in
        patch) NEW="$PATCH";;
        minor) NEW="$MINOR";;
        major) NEW="$MAJOR";;
        *) die "invalid choice: $choice";;
    esac
fi

# ---- guards ----
log "running go test ./..."
go test ./...

if [ "${SKIP_CI_CHECK:-}" = "1" ]; then
    log "SKIP_CI_CHECK=1 — skipping gh CI check"
else
    SHA="$(git rev-parse HEAD)"
    log "checking CI status for commit $SHA (workflow: ci.yml)"
    if ! gh run list --workflow ci.yml --branch main --commit "$SHA" \
         --json conclusion,status --limit 1 \
         | jq -e '.[0].status == "completed" and .[0].conclusion == "success"' >/dev/null; then
        die "ci.yml has not completed successfully for $SHA — wait, push more, or use SKIP_CI_CHECK=1"
    fi
fi

# ---- confirmation ----
if [ "${NONINTERACTIVE:-}" != "1" ]; then
    SHA="$(git rev-parse --short HEAD)"
    SUBJECT="$(git log -1 --pretty=%s)"
    echo
    echo "About to release $NEW"
    echo "  commit:  $SHA"
    echo "  subject: $SUBJECT"
    echo
    read -r -p "Continue? [y/N]: " confirm
    case "$confirm" in
        y|Y|yes|YES) ;;
        *) die "aborted";;
    esac
fi

# ---- tag + push ----
git tag -a "$NEW" -m "Release $NEW"
git push origin "$NEW"
log "tagged $NEW and pushed origin — CI will build the release"
