"""26+56+60+61: env: / $WORKSPACE propagation across every consumer, one boot.

Merges four near-identical env/workspace-propagation harnesses that each
cold-started their own VM to check overlapping angles of the same
mechanism: devm renders `env:` (plus the reserved WORKSPACE/IS_SANDBOX
keys) into .devm/.env, and install:, startup: (systemd services), and
direct exec all reach it via the with-devm-env wrapper sourcing that
file. One devm.yaml declares every probe up front; one cold-start proves
all of them.

Pins, in one boot:
  - $WORKSPACE expansion in a cfg.Env value at load time (was test_26).
  - Devm-injected WORKSPACE + IS_SANDBOX visible at the interactive shell
    prompt (was test_26).
  - PATH ergonomics: with-devm-env is on PATH, and a sub-command invoked
    through it inherits devm's persistent env (was test_26).
  - Bypass clean miss: a raw `tart exec NAME printenv` (no wrapper) shows
    NONE of devm's vars — the channel is wrapper-only (was test_26).
  - A user-declared `env:` kv (FROM_KIT_60) reaches install:, startup:,
    and direct wrapper exec (was test_60).
  - WORKSPACE reaches install:, startup:, and direct wrapper exec (was
    test_61).
  - Workspace *contents* (not just the env var) are visible inside the
    VM via the mount (was test_56).

What this doesn't cover (tested elsewhere):
  - env reaching an already-running VM via live `devm reconcile` (no
    recreate): test_11_env_inject_and_live_change. This merged test
    declares env: up front instead of reconciling it onto a running VM
    -- reconcile-without-recreate is test_11's job, not this one's.
  - Schema-rejection error messages (env.WORKSPACE reserved, unknown
    $VAR): Go unit tests in internal/schema/env_test.go.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers import Shell, stop_and_wait_stopped
from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm

EXPECTED_KIT = "kit-value-60"


@pytest.mark.timeout(180)
def test_env_and_workspace_propagation(workspace, devm, sandbox_name):
    # Sentinel file for mount-content visibility (56): written host-side
    # before cold-start, must be readable inside the VM at $WORKSPACE.
    sentinel = workspace.path / "MOUNT_SENTINEL_56"
    sentinel.write_text("present\n")

    ws = str(workspace.path)

    workspace.write_devmyaml(
        env={
            "FROM_KIT_60": EXPECTED_KIT,
            "CLAUDE_CONFIG_DIR": "$WORKSPACE/.claude",
        },
        install=[
            "printenv WORKSPACE > /tmp/install-ws 2>&1 || true",
            'printf "%s" "$FROM_KIT_60" > /tmp/install-mark-60',
        ],
        services={
            "wscheck": {
                "exec": ["sh", "-c", "printenv WORKSPACE > /tmp/startup-ws 2>&1 || true"],
                "restart": "no",
            },
            "envcheck": {
                "exec": ["sh", "-c", 'printf "%s" "$FROM_KIT_60" > /tmp/startup-mark-60'],
                "restart": "no",
            },
        },
    )

    # Owns cold-start: install: commands only run at first `devm shell`, so
    # the test itself triggers cold-start after devm.yaml is in place.
    r = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path), capture_output=True, timeout=300,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

    tart_sandbox = TartSandbox(name=sandbox_name)
    current = tart_sandbox.state()
    assert current == "running", f"expected VM running; got {current!r}"

    # ---- install: consumer (56's WORKSPACE probe + 60's kv probe). ----
    r = tart_sandbox.exec_shell("cat /tmp/install-ws")
    assert r.ok, f"install-ws missing: {r.stderr}"
    assert r.stdout.strip() == ws, (
        f"WORKSPACE in install: was {r.stdout.strip()!r}, expected {ws!r}"
    )

    r = tart_sandbox.exec_shell("cat /tmp/install-mark-60")
    assert r.ok, f"install-mark-60 missing: {r.stderr}"
    assert r.stdout == EXPECTED_KIT, (
        f"FROM_KIT_60 in install: was {r.stdout!r}, expected {EXPECTED_KIT!r}"
    )

    # ---- startup: (systemd service) consumer. ----
    r = tart_sandbox.exec_shell("cat /tmp/startup-ws")
    assert r.ok, f"startup-ws missing: {r.stderr}"
    assert r.stdout.strip() == ws, (
        f"WORKSPACE in startup: was {r.stdout.strip()!r}, expected {ws!r}"
    )

    r = tart_sandbox.exec_shell("cat /tmp/startup-mark-60")
    assert r.ok, f"startup-mark-60 missing: {r.stderr}"
    assert r.stdout == EXPECTED_KIT, (
        f"FROM_KIT_60 in startup: was {r.stdout!r}, expected {EXPECTED_KIT!r}"
    )

    # ---- direct exec via with-devm-env wrapper. ----
    r = tart_sandbox.exec_shell(
        'with-devm-env sh -c \'printf "%s" "$WORKSPACE"\''
    )
    assert r.ok, f"wrapper exec (WORKSPACE) failed: {r.stderr}"
    assert r.stdout == ws, f"WORKSPACE via with-devm-env was {r.stdout!r}"

    r = tart_sandbox.exec_shell(
        'with-devm-env sh -c \'printf "%s" "$FROM_KIT_60"\''
    )
    assert r.ok, f"wrapper exec (FROM_KIT_60) failed: {r.stderr}"
    assert r.stdout == EXPECTED_KIT, (
        f"FROM_KIT_60 via with-devm-env was {r.stdout!r}"
    )

    # ---- workspace mount content visibility (56). ----
    r = tart_sandbox.exec_shell(f"cat {ws}/MOUNT_SENTINEL_56")
    assert r.ok, f"workspace sentinel not visible at {ws}/MOUNT_SENTINEL_56: {r.stderr}"
    assert r.stdout.strip() == "present", (
        f"sentinel content unexpected: {r.stdout!r}"
    )

    # ---- Interactive shell checks (26): $WORKSPACE expansion in a cfg.Env
    # value, devm-injected WORKSPACE/IS_SANDBOX, PATH ergonomics, and a
    # sub-command invoked through the wrapper inheriting devm's env. Attaches
    # to the VM already cold-started above -- no extra boot.
    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=90)

        # $WORKSPACE expanded in cfg.Env value at load.
        sh.run_check(
            f'test "$CLAUDE_CONFIG_DIR" = "{ws}/.claude"',
            timeout=15,
        )

        # Devm-injected vars visible in shell (via wrapper sourcing .env).
        sh.run_check(f'test "$WORKSPACE" = "{ws}"', timeout=15)
        sh.run_check('test "$IS_SANDBOX" = "1"', timeout=15)

        # Wrapper is on PATH inside the shell.
        sh.run_check("command -v with-devm-env >/dev/null", timeout=15)

        # Sub-command via wrapper inherits devm's env. A fresh `sh -c`
        # would normally see only what its parent exported; the wrapper
        # sources .devm/.env first, so the sub-shell sees CLAUDE_CONFIG_DIR.
        sh.run_check(
            f'test "$(with-devm-env sh -c \'echo $CLAUDE_CONFIG_DIR\')" = "{ws}/.claude"',
            timeout=15,
        )

        sh.exit(timeout=30)

    # ---- Bypass clean miss (26): raw `tart exec NAME printenv` (no
    # wrapper) must NOT show any of devm's persistent vars. Proves they
    # live only in .devm/.env, sourced exclusively by the wrapper -- no
    # leak via the VM process env (which is deliberately kept clean).
    #
    # Run this AFTER exiting the user shell so the sandbox is still up
    # (anchor-alive) but there's no devm shell in the loop influencing
    # process env. A fresh tart exec attaches to the running VM and its
    # child sees only what the VM's own process env provides.
    result = tart_sandbox.exec("printenv", timeout=15)
    out = result.stdout

    # None of the devm-controlled vars must appear at all. Anchor each
    # match at line-start ("\n<KEY>=") so WORKSPACE doesn't false-match
    # against any WORKSPACE_DIR variant.
    haystack = "\n" + out
    for forbidden in (
        "\nCLAUDE_CONFIG_DIR=",
        "\nIS_SANDBOX=",
        "\nWORKSPACE=",
        "\nFROM_KIT_60=",
    ):
        assert forbidden not in haystack, (
            f"{forbidden.strip()!r} leaked outside the wrapper — devm env must "
            f"reach processes ONLY via with-devm-env sourcing .devm/.env. "
            f"Raw printenv output:\n{out}"
        )

    # Anchor-alive: explicitly stop after shell exit.
    stop_and_wait_stopped(devm, sandbox_name)
