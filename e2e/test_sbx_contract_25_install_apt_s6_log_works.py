"""install: apt install s6 succeeds on the shell base, and s6-log works.

Pins the building blocks for devm's startup-supervision feature
(docs/superpowers/specs/2026-06-07-startup-supervision-design.md):

  - `apt-get update && apt-get install -y s6` succeeds in the install:
    phase on docker/sandbox-templates:shell. Confirms the package is in
    Debian's repos and reachable through whatever network policy
    install: gets (contract_10 pins install: unrestricted).
  - `command -v s6-log` resolves after the install.
  - `s6-log` reads stdin and writes to a logdir: piping output produces
    a `current` file with the expected content.
  - Size-bounded rotation: writing >1 MB through `s6-log s1000000`
    archives the old current and starts a new one.

Devm dependency: the supervision feature pipes each user install:/
startup: command's stdout/stderr through s6-log to a per-step logdir at
/tmp/.devm/<phase>-<N>/{stdout,stderr}/. If apt install s6 doesn't work
or s6-log misbehaves on this base, the whole design needs to change
(bundled static binary, or homebrewed rotation, or alternate package).
"""
from __future__ import annotations

import time

import pytest

from helpers.contract import contract_sandbox, minimal_kit, sbx_exec

pytestmark = pytest.mark.sbx_contract


@pytest.mark.timeout(240)
def test_apt_install_s6_succeeds_and_s6_log_works(sandbox_name):
    # NOTE on the install-done marker: contract_sandbox waits for
    # status=running + exec-ready, but per the async-runtime gap pinned
    # by docs/superpowers/specs/2026-06-07-startup-supervision-design.md,
    # NEITHER means install: has actually completed. Apt-install of s6
    # takes ~20s; if we probed s6-log immediately after contract_sandbox
    # yielded, we'd get rc=127 because the install hasn't finished.
    #
    # Workaround: the LAST install step writes /tmp/install-done. The
    # test polls for that file before any other probe. This is the
    # install-marker scheme proposed in the startup-supervision design,
    # implemented inline here so this contract test doesn't depend on
    # the feature it's a prerequisite for.
    spec = minimal_kit(
        install=[
            "apt-get update",
            "DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends s6",
            "touch /tmp/install-done",
        ],
    )

    with contract_sandbox(spec, sandbox_name):
        # Wait for install: to complete via the trailing marker.
        deadline = time.monotonic() + 120
        while time.monotonic() < deadline:
            r = sbx_exec(sandbox_name, "test", "-f", "/tmp/install-done")
            if r.returncode == 0:
                break
            time.sleep(1.0)
        else:
            raise AssertionError(
                "install: did not complete (marker /tmp/install-done absent "
                "after 120s); apt install s6 likely failed or hung."
            )

        # 1. s6-log is on PATH after the apt install. `command -v` is a
        #    shell builtin, not an executable — wrap in `sh -c` so sbx
        #    exec actually runs it (otherwise OCI runtime errors out
        #    with "executable file `command` not found in $PATH").
        r = sbx_exec(sandbox_name, "sh", "-c", "command -v s6-log")
        assert r.returncode == 0, (
            f"s6-log not on PATH after `apt install -y s6`; "
            f"stdout={r.stdout.decode()!r} stderr={r.stderr.decode()!r}"
        )
        path = r.stdout.decode().strip()
        assert path, f"empty path from `command -v s6-log`: {r.stdout!r}"

        # 2. Basic write: pipe "hello\nworld\n" into s6-log, find it in
        #    the logdir's `current` file. -b is required for s6-log on a
        #    base without execline (just-pass-through directives).
        r = sbx_exec(
            sandbox_name, "sh", "-c",
            "rm -rf /tmp/probe-s6 && mkdir -p /tmp/probe-s6 && "
            "printf 'hello\\nworld\\n' | s6-log -b n3 s4096 /tmp/probe-s6 && "
            "cat /tmp/probe-s6/current",
        )
        assert r.returncode == 0, (
            f"basic s6-log write failed: stdout={r.stdout.decode()!r} "
            f"stderr={r.stderr.decode()!r}"
        )
        assert "hello" in r.stdout.decode() and "world" in r.stdout.decode(), (
            f"expected 'hello'+'world' in current file; got {r.stdout.decode()!r}"
        )

        # 3. Size-bounded rotation: write enough bytes to exceed the
        #    configured size and verify an archive file appears.
        #    s6-log size minimum is 4096 bytes; write ~20 KB to be
        #    safely above the limit.
        r = sbx_exec(
            sandbox_name, "sh", "-c",
            "rm -rf /tmp/probe-s6-rotate && mkdir -p /tmp/probe-s6-rotate && "
            "yes 'x' | head -c 20000 | s6-log -b n5 s4096 /tmp/probe-s6-rotate && "
            "ls /tmp/probe-s6-rotate",
        )
        assert r.returncode == 0, (
            f"rotation probe failed: stdout={r.stdout.decode()!r} "
            f"stderr={r.stderr.decode()!r}"
        )
        listing = r.stdout.decode()
        # At least one archived file (name starts with `@`) should exist
        # alongside the current file. Archived files use s6-log's
        # @timestamp.s naming.
        archived = [
            line for line in listing.splitlines()
            if line.startswith("@") and line.endswith(".s")
        ]
        assert archived, (
            f"expected at least one rotated archive after writing >4096 "
            f"bytes through s6-log s4096; listing was:\n{listing}"
        )
