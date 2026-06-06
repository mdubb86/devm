# justfile

# Build the devm binary into ./devm (for local testing).
build:
    go build -o devm ./cmd/devm

# Install the devm binary the canonical Go way → $GOBIN (or $GOPATH/bin).
# Typically lands at ~/go/bin/devm; add ~/go/bin to PATH if not already.
install:
    go install ./cmd/devm
    @bin="$(go env GOBIN)"; [ -n "$bin" ] || bin="$(go env GOPATH)/bin"; echo "installed to $bin/devm"
    @command -v devm >/dev/null || echo "(reminder: add $(go env GOPATH)/bin to PATH to invoke 'devm' directly)"

# Run Go unit tests.
test:
    go test ./...

# Remove build artifacts.
clean:
    rm -f devm

# Run the full e2e suite.
e2e:
    @e2e/scripts/run.sh

# Test groups by pytest marker. Pick one when you only care about a slice:
#   - devm:      tests that drive `devm` (the user-facing CLI flow)
#   - contract:  declarative sbx invariants devm depends on
#   - tripwire:  'broken' sbx behaviors devm works around (red = upstream fixed)
#   - probes:    Go-probe-driven async/timing sanity
e2e-devm:
    @e2e/scripts/run.sh -m devm

e2e-contract:
    @e2e/scripts/run.sh -m sbx_contract

e2e-tripwire:
    @e2e/scripts/run.sh -m sbx_tripwire

e2e-probes:
    @e2e/scripts/run.sh -m probe

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
