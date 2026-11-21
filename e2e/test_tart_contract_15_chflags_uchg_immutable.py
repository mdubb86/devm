"""Tart-contract e2e: prove `chflags uchg` on a host file inside a `tart --dir`
read-write share cannot be defeated by a ROOT user inside the guest.

This is NOT a test of devm's production code — it drives `tart` directly (no
devm daemon, no Go changes). It pins the platform primitive the devm.yaml
config-lock feature depends on: a host-side immutable flag on a file inside a
virtiofs share must hold even against a root guest, so a compromised/malicious
in-guest process can never rewrite devm.yaml to defeat its own sandbox.

A standalone spike already confirmed this manually (2026-07-19). This test
makes it a committed regression guard: if a future macOS/tart release changes
virtiofs's handling of BSD file flags (uchg), this fails loudly instead of
silently rotting the security property the feature rests on.
"""
import secrets
import subprocess
import tempfile
import time
from pathlib import Path

import pytest

from helpers import registry

TEMPLATE = "ghcr.io/cirruslabs/debian:latest"
MOUNTPOINT = "/mnt/spike"
DEVM_YAML_CONTENT = "allowlist:\n  - example.com\n  - github.com\n"
CODE_TXT_CONTENT = "print('hello')\n"


def _tart(*args, timeout=30):
    return subprocess.run(
        ["tart", *args], capture_output=True, text=True, timeout=timeout
    )


def _state(name: str) -> str:
    import json

    r = _tart("list", "--format=json", timeout=10)
    try:
        for e in json.loads(r.stdout):
            if e.get("Name") == name:
                return e.get("State", "")
    except Exception:
        pass
    return "absent"


def _gexec(name: str, script: str, timeout=45):
    # one retry on tart-guest-agent gRPC transport flakes
    for _ in range(2):
        r = subprocess.run(
            ["tart", "exec", name, "bash", "-c", script],
            capture_output=True, text=True, timeout=timeout,
        )
        if r.returncode == 0 or "Transport became inactive" not in r.stderr:
            return r
        time.sleep(2)
    return r


def _wait_exec_ready(name: str, timeout=60) -> bool:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if _gexec(name, "true").returncode == 0:
            return True
        time.sleep(1)
    return False


def _cleanup(name: str, proc):
    if proc is not None:
        proc.terminate()
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
    for _ in range(10):
        if _state(name) in ("stopped", "absent"):
            break
        time.sleep(1)
    subprocess.run(["tart", "delete", name], capture_output=True, timeout=15)


@pytest.mark.contract
def test_chflags_uchg_blocks_root_guest():
    name = f"e2e-contract-chflags-{secrets.token_hex(2)}"
    registry.append("sandbox", name)

    proc = None
    tmpdir_ctx = tempfile.TemporaryDirectory(prefix="devm-chflags-pin-")
    host_dir = Path(tmpdir_ctx.name)
    devm_yaml = host_dir / "devm.yaml"
    code_txt = host_dir / "code.txt"
    devm_yaml.write_text(DEVM_YAML_CONTENT)
    code_txt.write_text(CODE_TXT_CONTENT)

    try:
        # --- lock the file host-side, BEFORE the guest ever sees it ---
        flag = subprocess.run(
            ["chflags", "uchg", str(devm_yaml)], capture_output=True, text=True
        )
        assert flag.returncode == 0, f"chflags uchg failed: {flag.stderr!r}"

        _tart("clone", TEMPLATE, name, timeout=90)

        dir_arg = f"--dir={host_dir}:tag=spike"
        proc = subprocess.Popen(
            ["tart", "run", "--no-graphics", dir_arg, name],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        )

        deadline = time.monotonic() + 120
        while time.monotonic() < deadline and _state(name) != "running":
            time.sleep(2)
        assert _state(name) == "running", "VM never reached running"

        assert _wait_exec_ready(name), f"{name} never became exec-ready"

        mount = _gexec(
            name,
            f"sudo mkdir -p {MOUNTPOINT} && sudo mount -t virtiofs spike {MOUNTPOINT}",
        )
        assert mount.returncode == 0, (
            f"guest mount failed: rc={mount.returncode} "
            f"stdout={mount.stdout!r} stderr={mount.stderr!r}"
        )

        # --- 1. append is blocked, even as root ---
        r = _gexec(
            name,
            f"sudo sh -c 'echo x >> {MOUNTPOINT}/devm.yaml && echo OK || echo BLOCKED'",
        )
        assert "BLOCKED" in r.stdout, f"append should be blocked: {r.stdout!r} {r.stderr!r}"

        # --- 2. truncate is blocked ---
        # NOTE: `cp /dev/null <file>`, not shell `> file`. dash's own
        # redirection setup for `>` needs an O_TRUNC open, which fails
        # immediately against uchg; dash treats that as a shell-level error
        # and aborts the whole `sh -c` script before `&&`/`||` ever run — so
        # `echo BLOCKED` would never print. Routing the O_TRUNC open through
        # a subprocess (cp) keeps the failure inside the command being
        # tested, where `&&`/`||` can observe it.
        r = _gexec(
            name,
            f"sudo sh -c 'cp /dev/null {MOUNTPOINT}/devm.yaml && echo OK || echo BLOCKED'",
        )
        assert "BLOCKED" in r.stdout, f"truncate should be blocked: {r.stdout!r} {r.stderr!r}"

        # --- 3. root cannot even clear the immutable flag from inside the guest,
        # and a write attempted right after still fails ---
        r = _gexec(
            name,
            f"sudo sh -c 'chattr -i {MOUNTPOINT}/devm.yaml && echo OK || echo BLOCKED'",
        )
        assert "BLOCKED" in r.stdout, f"chattr -i should be blocked: {r.stdout!r} {r.stderr!r}"

        r = _gexec(
            name,
            f"sudo sh -c 'echo y >> {MOUNTPOINT}/devm.yaml && echo OK || echo BLOCKED'",
        )
        assert "BLOCKED" in r.stdout, (
            f"write after failed chattr -i should still be blocked: {r.stdout!r} {r.stderr!r}"
        )

        # --- 4. rm and mv are blocked ---
        r = _gexec(
            name,
            f"sudo sh -c 'rm -f {MOUNTPOINT}/devm.yaml && echo OK || echo BLOCKED'",
        )
        assert "BLOCKED" in r.stdout, f"rm should be blocked: {r.stdout!r} {r.stderr!r}"

        r = _gexec(
            name,
            f"sudo sh -c 'mv {MOUNTPOINT}/devm.yaml {MOUNTPOINT}/moved && echo OK || echo BLOCKED'",
        )
        assert "BLOCKED" in r.stdout, f"mv should be blocked: {r.stdout!r} {r.stderr!r}"

        # --- 5. reads still succeed (write-only protection) ---
        r = _gexec(name, f"cat {MOUNTPOINT}/devm.yaml")
        assert r.returncode == 0, f"cat should succeed: {r.stdout!r} {r.stderr!r}"
        assert r.stdout == DEVM_YAML_CONTENT, f"devm.yaml content changed: {r.stdout!r}"

        # --- 6. other files on the same (rw) share are unaffected ---
        r = _gexec(
            name,
            f"sudo sh -c 'echo added >> {MOUNTPOINT}/code.txt && echo OK || echo BLOCKED'",
        )
        assert "OK" in r.stdout, (
            f"code.txt write should succeed (share is rw; only devm.yaml is "
            f"locked): {r.stdout!r} {r.stderr!r}"
        )

        # --- host-side: content unchanged, flag still present ---
        assert devm_yaml.read_text() == DEVM_YAML_CONTENT, "host devm.yaml content changed"
        ls = subprocess.run(
            ["ls", "-lO", str(devm_yaml)], capture_output=True, text=True
        )
        assert "uchg" in ls.stdout, f"uchg flag missing host-side: {ls.stdout!r}"
    finally:
        _cleanup(name, proc)
        # nouchg before rmtree, or cleanup itself would fail on the locked file
        subprocess.run(["chflags", "nouchg", str(devm_yaml)], capture_output=True)
        tmpdir_ctx.cleanup()
        registry.remove("sandbox", name)
