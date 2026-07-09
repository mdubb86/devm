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

# ldflags that inject the git commit (with a -dirty suffix when the
# working tree has uncommitted changes) into main.Commit. The daemon
# reports this via /version, and `just doctor` compares the daemon's
# Commit to the working-tree binary's Commit to detect "you rebuilt
# but forgot to restart" without depending on file mtimes.
DEV_LDFLAGS := "-X main.Commit=$(git rev-parse --short=12 HEAD)$(git diff-index --quiet HEAD -- || echo -dirty)"

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

# Run the full e2e suite.
e2e:
    @e2e/scripts/run.sh

# Test groups by pytest marker. Pick one when you only care about a slice:
#   - devm:      tests that drive `devm` (the user-facing CLI flow)
#   - contract:  declarative tart + iron-proxy invariants devm depends on
e2e-devm:
    @e2e/scripts/run.sh -m devm

e2e-contract:
    @e2e/scripts/run.sh -m contract

# Exercise the docker recipe end-to-end. Slow (~5 min: installs Docker
# via get.docker.com in a fresh VM, pulls hello-world, runs a container).
# Kept separate from `e2e-devm` because it needs public-internet egress
# to Docker Hub and is expensive.
e2e-recipe-docker:
    @e2e/scripts/run.sh -m recipe_docker

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
