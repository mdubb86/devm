"""26: devm env wrapper — $WORKSPACE expansion, devm-injected vars, PATH ergonomics, bypass clean miss.

Pins the with-devm-env wrapper design end-to-end:
  1. $WORKSPACE in cfg.Env values expands to the absolute repo root
     at load time (internal/schema/env.go: ResolveEnv).
  2. Devm injects WORKSPACE + IS_SANDBOX into .devm/.env; both are
     visible at the shell prompt via the wrapper sourcing the file.
  3. PATH integration: `with-devm-env` resolves inside the shell, and
     invoking `with-devm-env <cmd>` gives the sub-command devm's
     persistent env (the wrapper sources .devm/.env on every call).
  4. Bypass clean miss: a raw `sbx exec NAME printenv` (no wrapper)
     shows NONE of devm's persistent vars — confirms the channel is
     wrapper-only, no kit-env leak. If devm.yaml's env: were ever
     accidentally routed via kit env, this test would fail (and
     simultaneously env edits would become BucketTeardownShell).

What this doesn't cover (tested elsewhere):
  - env reaching shell + live edit via reconcile:
    test_11_env_inject_and_live_change.
  - sbx-level kit env propagation to install/startup/exec:
    test_sbx_contract_21_env_kit_reaches_all_consumers.
  - sbx-level `sbx exec -e KEY=VAL` flag propagation (used today
    only for forwarded host vars TERM/COLORTERM/...):
    test_sbx_contract_22_env_exec_flag_propagates.
  - $WORKSPACE_DIR (set by sbx daemon, distinct from devm's $WORKSPACE):
    test_sbx_contract_17_install_workspace_dir_and_mount and
    test_sbx_contract_23_env_workspace_dir_set_by_sbx.
  - Schema-rejection error messages (env.WORKSPACE reserved, unknown
    $VAR): Go unit tests in internal/schema/env_test.go.
"""
import subprocess

import pytest

from helpers import Shell, stop_and_wait_stopped

pytestmark = pytest.mark.devm


@pytest.mark.timeout(120)
def test_env_wrapper_and_workspace(workspace, devm, sandbox_name):
    workspace.write_devmyaml(
        env={"CLAUDE_CONFIG_DIR": "$WORKSPACE/.claude"},
    )

    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=90)

        # 1. $WORKSPACE expanded in cfg.Env value at load.
        sh.run_check(
            f'test "$CLAUDE_CONFIG_DIR" = "{workspace.path}/.claude"',
            timeout=15,
        )

        # 2. Devm-injected vars visible in shell (via wrapper sourcing .env).
        sh.run_check(
            f'test "$WORKSPACE" = "{workspace.path}"',
            timeout=15,
        )
        sh.run_check('test "$IS_SANDBOX" = "1"', timeout=15)

        # 3a. Wrapper is on PATH inside the shell.
        sh.run_check("command -v with-devm-env >/dev/null", timeout=15)

        # 3b. Sub-command via wrapper inherits devm's env.
        #     A fresh `sh -c` would normally see only what its parent
        #     exported; the wrapper sources .devm/.env first, so the
        #     sub-shell sees CLAUDE_CONFIG_DIR.
        sh.run_check(
            f'test "$(with-devm-env sh -c \'echo $CLAUDE_CONFIG_DIR\')" = "{workspace.path}/.claude"',
            timeout=15,
        )

        sh.exit(timeout=30)

    # 4. Bypass clean miss: raw `sbx exec NAME printenv` (no wrapper) must
    #    NOT show any of devm's persistent vars. Proves they live only in
    #    .devm/.env, sourced exclusively by the wrapper — no leak via the
    #    kit env block (which is deliberately rendered empty).
    #
    #    We run this AFTER exiting the user shell so the sandbox is still
    #    up (anchor-alive) but there's no devm `sbx exec ... with-devm-env`
    #    in the loop influencing process env. A fresh sbx exec attaches to
    #    the running sandbox and its child sees only what the sandbox's
    #    own process env provides.
    out = subprocess.run(
        ["sbx", "exec", sandbox_name, "printenv"],
        capture_output=True, timeout=15, check=False,
    ).stdout.decode()

    # The three devm-controlled vars must NOT appear at all. Anchor each
    # match at line-start ("\n<KEY>=") so WORKSPACE doesn't false-match
    # against sbx's own WORKSPACE_DIR (separate var, set by sbx daemon,
    # pinned by test_sbx_contract_17/23 — that one IS expected to appear).
    haystack = "\n" + out
    for forbidden in ("\nCLAUDE_CONFIG_DIR=", "\nIS_SANDBOX=", "\nWORKSPACE="):
        assert forbidden not in haystack, (
            f"{forbidden.strip()!r} leaked outside the wrapper — devm env must "
            f"reach processes ONLY via with-devm-env sourcing .devm/.env. "
            f"Raw printenv output:\n{out}"
        )

    # Anchor-alive: explicitly stop after shell exit.
    stop_and_wait_stopped(devm, sandbox_name)
