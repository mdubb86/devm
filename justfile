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

# Build the devm binary into ./bin/devm and codesign with the local
# self-signed identity if available. The path matches what `devm
# install` records in the LaunchDaemon plist, so a rebuild swaps the
# binary in place — `devm service restart` picks it up.
build:
    @mkdir -p bin
    go build -ldflags "{{DEV_LDFLAGS}}" -o bin/devm ./cmd/devm
    @if security find-certificate -c '{{SIGN_IDENTITY}}' >/dev/null 2>&1; then \
        codesign --sign '{{SIGN_IDENTITY}}' --force --options=runtime bin/devm && \
        echo "signed with {{SIGN_IDENTITY}}"; \
    else \
        echo "warning: signing cert '{{SIGN_IDENTITY}}' not in keychain — every rebuild will re-prompt for keychain access"; \
        echo "         one-time fix: Keychain Access → Certificate Assistant → Create a Certificate (Name: {{SIGN_IDENTITY}}, Code Signing, Self Signed Root)"; \
    fi

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
e2e-devm:
    @e2e/scripts/run.sh -m "devm and not install"

e2e-install:
    @e2e/scripts/run.sh -m install

e2e-contract:
    @e2e/scripts/run.sh -m contract

# Exercise all recipe integrations end-to-end. Slow — each recipe's
# install (Docker via get.docker.com, whatever the next recipe needs)
# runs a real workload in a fresh VM. Kept out of `just e2e` because
# these need public-internet egress and take minutes per test.
e2e-recipe:
    @e2e/scripts/run.sh -m recipe

# Run a single test by name (matches pytest -k pattern). Foreground (no -n).
# Quote multi-word patterns: `just e2e-one "test_a or test_b"`.
e2e-one NAME:
    @e2e/scripts/run.sh -k '{{NAME}}' -n 0

# List discovered tests without running them.
e2e-list:
    cd e2e && uv sync --quiet && uv run pytest --collect-only -q

# Safety-net manual sweep of anything earlier runs left behind.
e2e-clean:
    @e2e/scripts/sweep-leftovers.sh

# Cut a release: interactive picker (patch/minor/major), runs all
# pre-tag guards, tags + pushes. CI takes over from there.
release:
    @scripts/release.sh

# Same as `release` but skips the e2e suite. Use for hotfixes.
release-no-e2e:
    @SKIP_E2E=1 scripts/release.sh

# Run goreleaser locally in dry-run mode against the current commit.
# Useful for validating .goreleaser.yaml without cutting a real release.
release-dry:
    goreleaser release --snapshot --clean --skip=publish

IRON_PROXY_VERSION := "v0.45.0"

# Download the pinned iron-proxy binary into ./bin/iron-proxy (dev layout).
# Skips if bin/iron-proxy already exists.
fetch-iron-proxy:
    @mkdir -p bin
    @if [ ! -f bin/iron-proxy ]; then \
      echo "Fetching iron-proxy {{IRON_PROXY_VERSION}}..." ; \
      ver="$(echo '{{IRON_PROXY_VERSION}}' | sed 's/^v//')" ; \
      curl -fsSL -o /tmp/iron-proxy.tar.gz \
        "https://github.com/ironsh/iron-proxy/releases/download/{{IRON_PROXY_VERSION}}/iron-proxy_${ver}_darwin_arm64.tar.gz" ; \
      tar -xzf /tmp/iron-proxy.tar.gz -C bin iron-proxy ; \
      chmod +x bin/iron-proxy ; \
      rm /tmp/iron-proxy.tar.gz ; \
    fi
    @echo "iron-proxy at bin/iron-proxy"
