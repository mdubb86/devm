# devm end-to-end tests

TTY-driven automated tests for `devm` commands. Replays the manual smoke checks as expect scripts.

## Prerequisites

- `expect` (Tcl). On macOS: `brew install expect`.
- `sbx` CLI installed and able to create sandboxes (`sbx ls` must work).

## Running

All tests:
```
make e2e
```

Single test:
```
DEVM_BIN=~/workspace/devm/devm \
  E2E_REGISTRY=$(mktemp -t devm-e2e-registry.XXXX) \
  expect test/e2e/01_cold_start.exp
```

(`make e2e` handles all this for you.)

## Adding a new test

1. Copy an existing `.exp` (e.g. `01_cold_start.exp`) as a template.
2. `source` `lib/common.exp` for setup/teardown helpers.
3. Call `e2e_setup_workspace <slug>` at the top — registers cleanup, returns `[list sandbox workspace]`.
4. Drive the scenario with `spawn`, `expect`, `send`.
5. Call `e2e_pass <test_name>` at the bottom on success.

## Isolation guarantees

- Each test gets a unique sandbox name: `e2e-test-<slug>-<random>` (4-hex suffix).
- Each test gets its own workspace under `/tmp/devm-e2e-<slug>-<random>/`.
- Cleanup is double-layered:
  - Tcl `exit` trap in `e2e_cleanup` runs whether the test succeeds, fails, times out, or hits an error.
  - Bash `trap` in `run-all.sh` reads a registry file and sweeps any entries that weren't cleaned by their per-test handler — catches SIGKILLed expect processes.
- Tests are NOT run in CI (need sbx); they're manual-only for now.
