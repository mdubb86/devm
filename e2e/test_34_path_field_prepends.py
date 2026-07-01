"""34: devm.yaml `path:` field prepends entries to $PATH inside the sandbox.

User declares a top-level `path:` list (added 2026-06-12). Each entry
is prepended to $PATH inside the sandbox via the same .devm/.env
fan-out cfg.Env uses, reaching all four executable entrypoints
(install, foreground startup, background startup, interactive shell).

This test pins the cold-start path: a single `$WORKSPACE/bin` entry
shows up at the head of $PATH in the interactive shell, with
$WORKSPACE having expanded to the repoRoot at config load time.

What this pins:
  - `path: ["$WORKSPACE/bin"]` resolves $WORKSPACE to the repo root
    (workspace.path) at load time.
  - The entry lands at the HEAD of $PATH inside the interactive shell
    (before devm-internal scripts and container defaults).

What it doesn't cover (tested elsewhere):
  - Live edit of path: via reconcile -> test_35.
  - Entry reaching install: / startup: entrypoints -> not yet pinned.
  - Validation rejection of disallowed entries (non-absolute, ~, $VAR)
    -> covered by schema unit tests.
"""
import pytest

from helpers import Shell, stop_and_wait_stopped

pytestmark = pytest.mark.devm


@pytest.mark.xfail(
    strict=False,
    reason=(
        "devm bug E: devm shell attaches bash directly via `tart exec vmName bash` "
        "without going through with-devm-env, so .devm/.env is not sourced in the "
        "interactive session. The path: field uses the same .devm/.env fan-out as "
        "cfg.Env, so path: entries are not prepended to $PATH in the shell. "
        "Remove xfail when bug E lands."
    ),
)
@pytest.mark.timeout(90)
def test_path_field_prepends(workspace, devm, sandbox_name):
    workspace.write_devmyaml(
        path=["$WORKSPACE/bin"],
    )

    expected_head = f"{workspace.path}/bin"

    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=60)

        # Verify $PATH starts with the expanded entry. Using awk so the
        # check is exact-prefix, not just substring (which could falsely
        # pass if the entry appeared mid-PATH).
        sh.run_check(
            f"awk -v p=\"$PATH\" -v want='{expected_head}:' "
            "'BEGIN{exit (index(p, want) == 1) ? 0 : 1}'",
            expect_zero=True, timeout=10,
        )

        sh.exit(timeout=30)

    stop_and_wait_stopped(devm, sandbox_name)
