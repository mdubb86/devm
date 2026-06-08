# justfile

# Build the devm-dev binary into ./devm-dev (for local testing).
# Named devm-dev so it never collides with a brew-installed `devm`.
build:
    go build -o devm-dev ./cmd/devm

# Install the working-tree build as `devm-dev` in $GOBIN (or $GOPATH/bin).
# The -dev suffix means it coexists with a brew-installed `devm` without
# any PATH games: `devm` → prod (brew), `devm-dev` → working tree.
install:
    @bin="$(go env GOBIN)"; [ -n "$bin" ] || bin="$(go env GOPATH)/bin"; \
        go build -o "$bin/devm-dev" ./cmd/devm && echo "installed $bin/devm-dev"
    @command -v devm-dev >/dev/null || echo "(reminder: add $(go env GOPATH)/bin to PATH so 'devm-dev' resolves)"

# Run Go unit tests.
test:
    go test ./...

# Remove build artifacts.
clean:
    rm -f devm-dev

# Run the full e2e suite.
e2e:
    @e2e/scripts/run.sh

# Test groups by pytest marker. Pick one when you only care about a slice:
#   - devm:      tests that drive `devm` (the user-facing CLI flow)
#   - contract:  declarative sbx invariants devm depends on
#   - tripwire:  'broken' sbx behaviors devm works around (red = upstream fixed)
#   - interop:   Go-primitive ↔ sbx combinations devm depends on
e2e-devm:
    @e2e/scripts/run.sh -m devm

e2e-contract:
    @e2e/scripts/run.sh -m sbx_contract

e2e-tripwire:
    @e2e/scripts/run.sh -m sbx_tripwire

e2e-interop:
    @e2e/scripts/run.sh -m sbx_interop

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
