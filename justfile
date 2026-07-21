# justfile

# Identity to sign local builds with. macOS keychain ACL is keyed by
# the signing identity, so signing each build with the SAME identity
# means one "Always Allow" click ever (vs. one per build with no
# stable identity).
#
# One-time setup (only needed for stable keychain access during dev):
#   open Keychain Access → Certificate Assistant → Create a Certificate
#     Name: devm-dev
#     Identity Type: Self Signed Root
#     Certificate Type: Code Signing
#
# If the cert doesn't exist, `just build` still produces a working
# binary; you'll just get keychain prompts on each rebuild.
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
        codesign --sign '{{SIGN_IDENTITY}}' --force --options=runtime "$daemon_out" "$helper_out" && \
        echo "signed with {{SIGN_IDENTITY}}"; \
    else \
        echo "warning: signing cert '{{SIGN_IDENTITY}}' not in keychain — every rebuild will re-prompt for keychain access"; \
        echo "         one-time fix: Keychain Access → Certificate Assistant → Create a Certificate (Name: {{SIGN_IDENTITY}}, Code Signing, Self Signed Root)"; \
    fi

# Build the devm + devm-helper binaries into ./bin with prod identity,
# and codesign with the local self-signed identity if available. The
# path matches what `devm install` records in the LaunchDaemon plist,
# so a rebuild swaps the binary in place — `devm service restart`
# picks it up.
#
# fetch-iron-proxy runs first: the ironproxy package's //go:embed
# needs internal/ironproxy/embed/iron-proxy.gz to exist at compile time.
build: fetch-iron-proxy (_build "prod")

# Build the devm-e2e + devm-e2e-helper binaries into ./bin with e2e
# identity, so they run alongside — not clobber — a live prod install
# (separate runtime dir, socket, LaunchDaemon label; see internal/identity).
build-e2e: fetch-iron-proxy (_build "e2e")

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

# Run install-marker tests. Tests manage their own binary placement,
# sudo escalation, and install/uninstall lifecycle.
e2e-install *NAMES: (_build "e2e")
    #!/usr/bin/env bash
    set -uo pipefail
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
e2e-bootstrap: (_build "e2e")
    @sudo -v
    @sudo install -m 755 bin/devm-e2e        /usr/local/bin/devm-e2e
    @sudo install -m 755 bin/devm-e2e-helper /usr/local/bin/devm-e2e-helper
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
    goreleaser release --snapshot --clean --skip=publish

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
