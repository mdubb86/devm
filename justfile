# justfile

# Run the full e2e suite in parallel (2 workers).
e2e:
    @e2e/scripts/run.sh

# Run a single test by name (matches pytest -k pattern). Foreground (no -n).
e2e-one NAME:
    @e2e/scripts/run.sh -k {{NAME}} -n 0

# List discovered tests without running them.
e2e-list:
    cd e2e && uv sync --quiet && uv run pytest --collect-only -q

# Safety-net manual sweep of anything earlier runs left behind.
e2e-clean:
    @e2e/scripts/sweep-leftovers.sh
