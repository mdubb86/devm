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

# Run the full e2e suite in parallel (2 workers).
e2e:
    @e2e/scripts/run.sh

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
