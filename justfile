# justfile

# Identity to sign local builds with. macOS keychain ACL is keyed by
# the signing identity, so signing each build with the SAME identity
# means one "Always Allow" click ever (vs. one per build with no
# stable identity).
#
# `_build` passes --identifier com.mdubb86.devm (and .helper) to
# codesign so macOS TCC treats every rebuild as the SAME app — one
# Full Disk Access entry instead of one per rebuild. Without this,
# codesign auto-derives the identifier from the binary name (Go's
# linker-signed default is literally "a.out") and TCC accumulates a
# fresh entry every install.
#
# One-time setup (only needed for stable keychain access during dev):
#   open Keychain Access → Certificate Assistant → Create a Certificate
#     Name: devm-dev
#     Identity Type: Self Signed Root
#     Certificate Type: Code Signing
#
# If the cert doesn't exist, `just build` still produces a working
# binary, still with the stable --identifier, just ad-hoc signed
# instead of self-signed — so TCC entries still collapse to one.
SIGN_IDENTITY := "devm-dev"

# ldflags injected into every dev build:
#
#  - main.Commit — git rev + `-dirty` when the working tree has
#    uncommitted changes. Reported via /version; useful for humans
#    grepping the daemon logs.
#
#  - main.Fingerprint — random per-build stamp. The CLI and the
#    installed daemon share this value (both compiled from the same
#    `go build` invocation); a mismatch at command time means the
#    on-disk binary has been rebuilt since the daemon last started,
#    and the CLI raises an error telling the user to `devm install`.
#    Cheap runtime check — string equality against a compiled-in
#    constant — but only meaningful if EVERY build injects a fresh
#    Fingerprint, hence the injection here, in .goreleaser.yaml, and
#    in e2e/scripts/run.sh's `go build` too.
DEV_LDFLAGS := "-X main.Commit=$(git rev-parse --short=12 HEAD)$(git diff-index --quiet HEAD -- || echo -dirty) -X main.Fingerprint=$(head -c 8 /dev/urandom | xxd -p)"

# Private: build both binaries for the given profile.
# NOTE: this recipe body is one joined shell command. Build failures
# MUST be explicit (`|| exit 1`) — otherwise a later step like the
# codesign `if` block's exit code masks them.
_build PROFILE:
    @mkdir -p bin internal/docker/embed
    GOOS=linux GOARCH=arm64 go build -o internal/docker/embed/devm-runc-shim   ./cmd/devm-runc-shim
    GOOS=linux GOARCH=arm64 go build -o internal/docker/embed/devm-docker-shim ./cmd/devm-docker-shim
    @case "{{PROFILE}}" in \
        prod) daemon_out=bin/devm;     helper_out=bin/devm-helper ;; \
        e2e)  daemon_out=bin/devm-e2e; helper_out=bin/devm-e2e-helper ;; \
        *)    echo "unknown profile: {{PROFILE}}" >&2; exit 1 ;; \
    esac; \
    ldflags="{{DEV_LDFLAGS}} -X github.com/mdubb86/devm/internal/identity.Profile={{PROFILE}}"; \
    go build -ldflags "$ldflags" -o "$daemon_out" ./cmd/devm && \
    go build -ldflags "$ldflags" -o "$helper_out" ./cmd/devm-helper || exit 1; \
    if security find-certificate -c '{{SIGN_IDENTITY}}' >/dev/null 2>&1; then \
        codesign --sign '{{SIGN_IDENTITY}}' --force --options=runtime --identifier com.mdubb86.devm        "$daemon_out" && \
        codesign --sign '{{SIGN_IDENTITY}}' --force --options=runtime --identifier com.mdubb86.devm.helper "$helper_out" && \
        echo "signed with {{SIGN_IDENTITY}}"; \
    else \
        echo "warning: signing cert '{{SIGN_IDENTITY}}' not in keychain — falling back to ad-hoc sign (still with stable --identifier)"; \
        echo "         one-time fix: Keychain Access → Certificate Assistant → Create a Certificate (Name: {{SIGN_IDENTITY}}, Code Signing, Self Signed Root)"; \
        codesign --sign - --force --options=runtime --identifier com.mdubb86.devm        "$daemon_out" && \
        codesign --sign - --force --options=runtime --identifier com.mdubb86.devm.helper "$helper_out"; \
    fi

# Build the darwin/arm64 devm-helper binary for PROFILE ("prod" or
# "e2e") and gzip it into the embed directory. `//go:embed
# embed/devm-helper.gz` in internal/helper/embed.go requires this file
# at compile time; `_build` depends on this recipe. Rebuilds on every
# invocation (fast; devm-helper is small) so any change to
# cmd/devm-helper/ is reflected in the next devm build without needing
# a manual clean.
#
# Must inject identity.Profile via -ldflags, same as `_build` does for
# the daemon/CLI binaries — otherwise the embedded helper always runs
# under the default Profile="prod" (socket /var/run/devm-helper.sock,
# group _devm, lo0 pool 1-20) regardless of which profile embedded it,
# cross-contaminating prod and e2e installs.
#
# NOTE: `just build` and `just build-e2e` both write to the same
# internal/helper/embed/devm-helper.gz — last one run wins. Acceptable:
# the on-disk bin/ output is the canonical per-profile artifact: the
# blob only matters for the immediately-following `_build` in the same
# recipe chain.
_build-helper-embed PROFILE:
    @mkdir -p internal/helper/embed
    @raw="$(mktemp -t devm-helper-raw.XXXXXX)" && \
      GOOS=darwin GOARCH=arm64 go build \
          -ldflags "-X github.com/mdubb86/devm/internal/identity.Profile={{PROFILE}}" \
          -o "$raw" ./cmd/devm-helper && \
      gzip -c "$raw" > internal/helper/embed/devm-helper.gz && \
      rm "$raw"
    @echo "devm-helper ({{PROFILE}}) embedded at internal/helper/embed/devm-helper.gz"

# Build the devm-setsid-shim binary for darwin/arm64 and gzip it
# into internal/setsidshim/embed/. `//go:embed embed/devm-setsid-shim.gz`
# in internal/setsidshim/embed.go requires this file at compile time;
# `_build` depends on this recipe. Identity-agnostic (no ldflags).
_build-setsid-shim-embed:
    @mkdir -p internal/setsidshim/embed
    @raw="$(mktemp -t devm-setsid-shim-raw.XXXXXX)" && \
      GOOS=darwin GOARCH=arm64 go build -o "$raw" ./cmd/devm-setsid-shim && \
      gzip -c "$raw" > internal/setsidshim/embed/devm-setsid-shim.gz && \
      rm "$raw"
    @echo "devm-setsid-shim embedded at internal/setsidshim/embed/devm-setsid-shim.gz"

# Build the devm-runc-shim + devm-docker-shim binaries for linux/arm64
# and drop them into internal/docker/embed/. `//go:embed
# embed/devm-runc-shim` and `//go:embed embed/devm-docker-shim` in
# internal/docker/embed.go require these files at compile time.
# `_build` inlines this build (it's part of the same shell chain that
# does the codesign), but this standalone recipe exists so `embeds`
# and CI can prep the docker embeds without invoking the full `_build`
# (which would also compile + codesign the main devm binary — Mac-only
# concerns unnecessary for `go test`/`go vet`).
_build-docker-shims-embed:
    @mkdir -p internal/docker/embed
    GOOS=linux GOARCH=arm64 go build -o internal/docker/embed/devm-runc-shim   ./cmd/devm-runc-shim
    GOOS=linux GOARCH=arm64 go build -o internal/docker/embed/devm-docker-shim ./cmd/devm-docker-shim

# Build the guest-side pop binary (linux-arm64) into the guestbin
# embed directory. `just embeds` and `build`/`build-e2e` depend on
# this so //go:embed can compile.
_build-pop-embed:
    @mkdir -p internal/guestbin/embed
    GOOS=linux GOARCH=arm64 go build -o internal/guestbin/embed/pop ./cmd/pop
    @echo "pop embedded at internal/guestbin/embed/pop"

# Prep every `//go:embed` blob the daemon needs, so subsequent `go
# test` / `go vet` / `go build` can compile. This is the "just embed
# prep, no main binary compile" recipe — used by CI (which only needs
# to run tests, doesn't need bin/devm) and by scripts/release.sh
# (which runs `go test ./...` as a pre-tag guard). `_build-helper-embed`
# uses the prod identity here; `build-e2e` overrides with an "e2e"
# helper build afterwards for local e2e installs.
embeds: fetch-iron-proxy (_build-helper-embed "prod") _build-setsid-shim-embed _build-docker-shims-embed _build-pop-embed

# Build the devm + devm-helper binaries into ./bin with prod identity,
# and codesign with the local self-signed identity if available. The
# path matches what `devm install` records in the LaunchDaemon plist,
# so a rebuild swaps the binary in place — `devm service restart`
# picks it up.
#
# fetch-iron-proxy runs first: the ironproxy package's //go:embed
# needs internal/ironproxy/embed/iron-proxy.gz to exist at compile time.
build: fetch-iron-proxy (_build-helper-embed "prod") (_build-setsid-shim-embed) (_build-pop-embed) (_build "prod")

# Build the devm-e2e + devm-e2e-helper binaries into ./bin with e2e
# identity, so they run alongside — not clobber — a live prod install
# (separate runtime dir, socket, LaunchDaemon label; see internal/identity).
build-e2e: fetch-iron-proxy (_build-helper-embed "e2e") (_build-setsid-shim-embed) (_build-pop-embed) (_build "e2e")

# Run Go unit tests.
test:
    go test ./...

# Remove build artifacts.
clean:
    rm -rf bin/

# Test groups by pytest marker. Pick one when you only care about a slice:
#   - devm:      exercises devm's features (using devm)
#   - install:   exercises devm's install lifecycle (installing devm)
#   - contract:  declarative tart + iron-proxy invariants devm depends on
#   - recipe:    end-to-end pins for a specific recipe (Docker, etc.)
#
# Both recipes below accept zero or more test-name patterns: no args
# runs the full marker slice, one or more args become a pytest -k
# filter (OR-joined). Matching zero tests is a hard failure.

# Run devm e2e tests. No args = full suite. Args = pytest -k filter.
# Requires bootstrap state. Hard-fails if NAMES match nothing.
e2e *NAMES:
    #!/usr/bin/env bash
    set -uo pipefail
    scripts/assert-e2e-installed.sh || {
        echo "e2e state not bootstrapped. Run: just e2e-bootstrap"
        exit 1
    }
    args=(-m "devm and not install")
    [ -n "{{NAMES}}" ] && args+=(-k "$(echo '{{NAMES}}' | sed 's/ / or /g')")
    e2e/scripts/run.sh "${args[@]}"
    rc=$?
    if [ $rc -eq 5 ] && [ -n "{{NAMES}}" ]; then
        echo "no tests matched: {{NAMES}}" >&2; exit 1
    fi
    exit $rc

# Run contract-marker e2e tests: pin upstream tool invariants (tart,
# iron-proxy, mutagen) devm depends on. No devm daemon needed; contract
# tests run the upstream binary directly against their own tmp state.
# Hard-fails if NAMES match nothing.
e2e-contract *NAMES:
    #!/usr/bin/env bash
    set -uo pipefail
    args=(-m contract)
    [ -n "{{NAMES}}" ] && args+=(-k "$(echo '{{NAMES}}' | sed 's/ / or /g')")
    e2e/scripts/run.sh "${args[@]}"
    rc=$?
    if [ $rc -eq 5 ] && [ -n "{{NAMES}}" ]; then
        echo "no tests matched: {{NAMES}}" >&2; exit 1
    fi
    exit $rc

# Run recipe-marker e2e tests (exercises devm's recipes end-to-end:
# installs the tool, brings up a real workload, asserts the recipe's
# promises hold). Same bootstrap requirement as `just e2e`; no sudo.
# Slow — each recipe test installs the target tool + hydrates its
# runtime, so runtimes are minutes to tens of minutes.
e2e-recipe *NAMES:
    #!/usr/bin/env bash
    set -uo pipefail
    scripts/assert-e2e-installed.sh || {
        echo "e2e state not bootstrapped. Run: just e2e-bootstrap"
        exit 1
    }
    args=(-m recipe)
    [ -n "{{NAMES}}" ] && args+=(-k "$(echo '{{NAMES}}' | sed 's/ / or /g')")
    e2e/scripts/run.sh "${args[@]}"
    rc=$?
    if [ $rc -eq 5 ] && [ -n "{{NAMES}}" ]; then
        echo "no tests matched: {{NAMES}}" >&2; exit 1
    fi
    exit $rc

# Run install-marker tests. Each test exercises `devm-e2e install`/
# `devm-e2e uninstall` themselves; those invocations need sudo. Prime
# the timestamp up-front, then spawn a background refresher that keeps
# it warm for the whole run — macOS's default 5-min sudo timestamp
# would otherwise expire mid-suite, and the subprocess `capture_output`
# swallows the resulting `Password:` prompt with no way to type at it.
# The refresher is killed on any exit path (normal, error, ^C via
# trap).
e2e-install *NAMES: (_build-helper-embed "e2e") (_build-setsid-shim-embed) (_build "e2e")
    #!/usr/bin/env bash
    set -uo pipefail
    sudo -v
    ( while true; do sudo -n -v 2>/dev/null || exit; sleep 60; done ) &
    sudo_pid=$!
    trap 'kill "$sudo_pid" 2>/dev/null || true' EXIT INT TERM
    args=(-m install)
    [ -n "{{NAMES}}" ] && args+=(-k "$(echo '{{NAMES}}' | sed 's/ / or /g')")
    e2e/scripts/run.sh "${args[@]}"
    rc=$?
    if [ $rc -eq 5 ] && [ -n "{{NAMES}}" ]; then
        echo "no tests matched: {{NAMES}}" >&2; exit 1
    fi
    exit $rc

# Build & install the parallel e2e devm. Idempotent-forward: always
# ends in installed-and-running. First run prompts for TouchID (plist,
# resolver file, keychain, lo0 aliases, group, base image build).
# Doubles as the canonical single-scenario install test.
e2e-bootstrap: (_build-helper-embed "e2e") (_build-setsid-shim-embed) (_build "e2e")
    @sudo -v
    @sudo install -m 755 bin/devm-e2e /usr/local/bin/devm-e2e
    /usr/local/bin/devm-e2e install
    @scripts/assert-e2e-installed.sh

# Uninstall the parallel e2e devm and assert every trace is gone.
# Doubles as the canonical single-scenario uninstall test.
e2e-teardown:
    @sudo -v
    @if [ -x /usr/local/bin/devm-e2e ]; then \
        /usr/local/bin/devm-e2e uninstall; \
    fi
    @sudo rm -f /usr/local/bin/devm-e2e /usr/local/bin/devm-e2e-helper
    @scripts/assert-e2e-uninstalled.sh

# List discovered tests without running them.
e2e-list:
    cd e2e && uv sync --quiet && uv run pytest --collect-only -q

# Safety-net manual sweep of anything earlier runs left behind.
e2e-clean:
    @e2e/scripts/sweep-leftovers.sh

# Cut a release: interactive picker (patch/minor/major), runs unit
# tests + gh CI-green check, tags + pushes. CI takes over from there.
# `just e2e` is a manual pre-release step — it needs sudo/Touch
# ID and can't run under the release script's shell.
release:
    @scripts/release.sh

# Run goreleaser locally in dry-run mode against the current commit.
# Useful for validating .goreleaser.yaml without cutting a real release.
release-dry:
    FINGERPRINT="${FINGERPRINT:-devlocal}" goreleaser release --snapshot --clean --skip=publish

IRON_PROXY_VERSION := "v0.45.0"

# Download the pinned iron-proxy binary and gzip it into the embed
# directory. `//go:embed embed/iron-proxy.gz` in internal/ironproxy/embed.go
# requires this file at compile time; `just build` depends on this
# recipe. Skips the download when the gzipped blob is already present.
fetch-iron-proxy:
    @mkdir -p internal/ironproxy/embed
    @if [ ! -f internal/ironproxy/embed/iron-proxy.gz ]; then \
      echo "Fetching iron-proxy {{IRON_PROXY_VERSION}}..." ; \
      ver="$(echo '{{IRON_PROXY_VERSION}}' | sed 's/^v//')" ; \
      curl -fsSL -o /tmp/iron-proxy.tar.gz \
        "https://github.com/ironsh/iron-proxy/releases/download/{{IRON_PROXY_VERSION}}/iron-proxy_${ver}_darwin_arm64.tar.gz" ; \
      tar -xzf /tmp/iron-proxy.tar.gz -C /tmp iron-proxy ; \
      gzip -c /tmp/iron-proxy > internal/ironproxy/embed/iron-proxy.gz ; \
      rm /tmp/iron-proxy.tar.gz /tmp/iron-proxy ; \
    fi
    @echo "iron-proxy embedded at internal/ironproxy/embed/iron-proxy.gz"
    @mkdir -p bin
    @gunzip -kc internal/ironproxy/embed/iron-proxy.gz > bin/iron-proxy
    @chmod +x bin/iron-proxy
    @echo "iron-proxy binary extracted to bin/iron-proxy"

# Fetch mutagen binary + agents bundle for embed. Pinned to v0.18.1.
# The agents tarball ships every guest-platform agent binary mutagen
# knows how to install; the CLI looks for it alongside the mutagen
# binary and fails 'unable to locate agent bundle' without it.
fetch-mutagen:
    #!/usr/bin/env bash
    set -euo pipefail
    VER=v0.18.1
    ARCH=arm64
    URL="https://github.com/mutagen-io/mutagen/releases/download/${VER}/mutagen_darwin_${ARCH}_${VER}.tar.gz"
    DEST=internal/mutagen/embed
    mkdir -p "$DEST"
    tmp=$(mktemp -d)
    trap "rm -rf $tmp" EXIT
    curl -fsSL "$URL" -o "$tmp/mutagen.tar.gz"
    tar -xzf "$tmp/mutagen.tar.gz" -C "$tmp" mutagen mutagen-agents.tar.gz
    gzip -c "$tmp/mutagen" > "$DEST/mutagen.gz"
    cp "$tmp/mutagen-agents.tar.gz" "$DEST/mutagen-agents.tar.gz"
    echo "wrote $DEST/mutagen.gz ($(wc -c < $DEST/mutagen.gz) bytes)"
    echo "wrote $DEST/mutagen-agents.tar.gz ($(wc -c < $DEST/mutagen-agents.tar.gz) bytes)"
    # Also stage runnable copies under bin/ for contract tests, mirroring
    # bin/iron-proxy. Not used by production (production reads the embed
    # blob via internal/mutagen.Ensure); contract tests need an executable
    # they can run without a devm-e2e bootstrap.
    mkdir -p bin
    cp "$tmp/mutagen" bin/mutagen
    chmod +x bin/mutagen
    cp "$tmp/mutagen-agents.tar.gz" bin/mutagen-agents.tar.gz
    echo "wrote bin/mutagen + bin/mutagen-agents.tar.gz for contract tests"
