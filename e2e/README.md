# devm e2e tests

Python + pexpect + pytest harness for devm end-to-end tests.

## Run

All tests (parallel, 2 workers):

```
just e2e
```

A single test (foreground, no parallelism):

```
just e2e-one NAME=test_01_cold_start
```

List discovered tests:

```
just e2e-list
```

Manual cleanup of leftovers from a hard-killed prior run:

```
just e2e-clean
```

## Layout

- `pyproject.toml` — uv project, deps, pytest config.
- `conftest.py` — shared fixtures (workspace, devm, sandbox_name, tart_sandbox, inspector_vm).
- `helpers/` — Python modules wrapping devm / tart / iron-proxy / pexpect.
- `scripts/run.sh` — bash wrapper that owns the registry file + signal escalation + pytest invocation.
- `test_NN_*.py` — devm-driver scenarios (one file per scenario).
- `test_tart_contract_*.py`, `test_iron_contract_*.py` — pure upstream contract pins (no devm).
- `tests/` — offline unit tests for the helpers (no tart, no devm).

## Conventions

- Each test gets a unique sandbox name + workspace. Fixtures register these before creating; cleanup happens on test end (or via the bash wrapper sweep if pytest is hard-killed).
- Set `@pytest.mark.timeout(N)` based on observed time: run the new test once with the default 120s ceiling, see how long it takes, set `N = observed + ~50%`. Operation-level timeouts already live in the helpers.
- Use `Shell.run_check(cmd, expect_zero=True)` for in-VM assertions — it uses an `echo "TAG=$?"` pattern internally to dodge command-echo desync.
