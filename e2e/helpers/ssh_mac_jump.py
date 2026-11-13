"""Helpers for the ssh-via-mac-jump e2e tests.

Composes the existing Workspace + Devm + TartSandbox into one
context-managed Project handle so a test can declare its ssh: list,
cold-start, run commands in the guest, and run commands on the Mac
side without rewiring the pytest fixture graph for every scenario
(the stock `workspace`/`devm` fixtures are scoped to one project
per test; the deny test wants two live projects at once).

Per-project jump client key path (identity.E2E.RuntimeDir()):
  ~/Library/Application Support/devm-e2e/ssh/<projectID>.jump.priv
See internal/serviceapi/sshjump_key.go's LoadOrCreateProjectJumpKey.
"""
from __future__ import annotations

import contextlib
import os
import secrets
import shutil
import subprocess
import sys
import tempfile
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Iterator

import yaml

from . import registry
from .devm import Devm
from .tart import TartSandbox
from .workspace import Workspace

_RUNTIME_DIR = Path.home() / "Library" / "Application Support" / "devm-e2e"
_DEVM_E2E_BIN = "/usr/local/bin/devm-e2e"


@dataclass
class _Result:
    """subprocess.CompletedProcess-shaped result for run_in_guest."""
    stdout: str
    stderr: str
    returncode: int


class Project:
    """One devm-e2e project handle: Workspace + Devm + TartSandbox."""

    def __init__(self, label: str):
        name = f"e2e-{label}-{secrets.token_hex(2)}"
        self.vm_name = name
        self.slug = name
        self._path = Path(tempfile.mkdtemp(prefix=f"devm-e2e-{name}-")).resolve()
        registry.append("workspace", str(self._path))
        registry.append("sandbox", name)
        self._workspace = Workspace(self._path, slug=name, vm_name=name)
        # Default: minimal config with no fixture repo. Tests patch in
        # the ssh: list (and anything else) before start().
        self._workspace.write_devmyaml(no_repo=True)
        self._devm = Devm(_DEVM_E2E_BIN, cwd=str(self._path))
        self._tart = TartSandbox(name=name)

    @property
    def path(self) -> Path:
        return self._path

    @property
    def client_key_path(self) -> Path:
        """Mac-side path to this project's per-project devm-jump client
        private key, the key Mac's ssh-agent needs to serve so the guest's
        IdentityAgent bridge has something to return. Written by the
        daemon on first start (internal/serviceapi/sshjump_key.go)."""
        return _RUNTIME_DIR / "ssh" / f"{self.vm_name}.jump.priv"

    def set_devm_yaml(self, patch: dict[str, Any]) -> None:
        """Patch devm.yaml. The `ssh` key routes into project.ssh since
        the schema nests it under project (internal/schema/schema.go
        Project.SSH); every other key is a top-level section."""
        yaml_path = self._workspace.devm_yaml_path
        cfg = yaml.safe_load(yaml_path.read_text()) or {}
        for k, v in patch.items():
            if k == "ssh":
                project = cfg.setdefault("project", {"name": self.vm_name})
                project["ssh"] = v
            else:
                cfg[k] = v
        yaml_path.write_text(yaml.safe_dump(cfg, sort_keys=False))

    def start(self, timeout: float = 480.0) -> None:
        """Cold-start the project VM via `devm start`. Raises on non-zero."""
        r = subprocess.run(
            [self._devm.path, "start"],
            cwd=str(self._path), capture_output=True, timeout=timeout,
        )
        if r.returncode != 0:
            raise AssertionError(
                f"devm start for {self.vm_name} failed (exit {r.returncode}):\n"
                f"stdout={r.stdout.decode(errors='replace')!r}\n"
                f"stderr={r.stderr.decode(errors='replace')!r}"
            )

    def run_in_guest(
        self,
        argv: list[str],
        *,
        env: dict[str, str] | None = None,
        check: bool = True,
        timeout: float = 60.0,
    ) -> _Result:
        """Run `argv` inside the guest via `tart exec`, returning a
        CompletedProcess-shaped object (stdout, stderr, returncode).

        `env` is applied via a `env K=V ...` prefix so only the one
        command sees it (tart exec propagates the caller's env wholesale;
        prefixing is the clean, scoped way)."""
        full = list(argv)
        if env:
            prefix = ["env"] + [f"{k}={v}" for k, v in env.items()]
            full = prefix + full
        r = self._tart.exec(*full, timeout=timeout)
        res = _Result(stdout=r.stdout, stderr=r.stderr, returncode=r.exit_code)
        if check and res.returncode != 0:
            raise AssertionError(
                f"guest cmd {argv} in {self.vm_name} failed "
                f"(exit {res.returncode}):\nstdout={res.stdout!r}\n"
                f"stderr={res.stderr!r}"
            )
        return res

    def mac_cmd(
        self,
        argv: list[str],
        *,
        check: bool = False,
        timeout: float = 30.0,
    ) -> subprocess.CompletedProcess:
        """Run `argv` on the Mac side (plain subprocess)."""
        return subprocess.run(
            argv, capture_output=True, text=True, timeout=timeout, check=check,
        )

    def teardown(self) -> None:
        """Stop the VM, remove its state, and drop registry entries.
        Best-effort — if the daemon is down or the sandbox is already
        absent, teardown noops. Exceptions are logged to stderr (not
        swallowed silently) so a wedged teardown surfaces in the test
        output."""
        try:
            subprocess.run(
                [self._devm.path, "teardown", "--yes"],
                cwd=str(self._path), capture_output=True, timeout=120,
            )
        except Exception as e:
            print(
                f"ssh_mac_jump teardown: devm teardown {self.vm_name} "
                f"failed: {e!r}",
                file=sys.stderr,
            )
        try:
            self._workspace.teardown()
        except Exception as e:
            print(
                f"ssh_mac_jump teardown: workspace.teardown "
                f"{self.vm_name} failed: {e!r}",
                file=sys.stderr,
            )
        shutil.rmtree(self._path, ignore_errors=True)
        registry.remove("sandbox", self.vm_name)
        registry.remove("workspace", str(self._path))


@contextlib.contextmanager
def e2e_project(label: str) -> Iterator[Project]:
    """Context-managed Project: register for cleanup, yield handle,
    guarantee teardown. The deny test uses two of these concurrently."""
    p = Project(label)
    try:
        yield p
    finally:
        p.teardown()


@contextlib.contextmanager
def ssh_add_and_cleanup(key_path: Path | str) -> Iterator[None]:
    """Load a key into Mac's ssh-agent for the test, remove on exit.

    Uses `ssh-add -d <pubkey-file>` on exit, writing the derived public
    key to a tempfile first (ssh-add -d takes the pubkey path, not the
    privkey). cleanup is best-effort — a leftover identity in a
    persistent agent is a small leak, not a test failure."""
    key_path = str(key_path)
    subprocess.run(["ssh-add", key_path], check=True, capture_output=True)
    pub = subprocess.run(
        ["ssh-keygen", "-y", "-f", key_path],
        check=True, capture_output=True,
    ).stdout
    try:
        yield
    finally:
        pub_file = tempfile.NamedTemporaryFile("wb", delete=False, suffix=".pub")
        try:
            pub_file.write(pub)
            pub_file.close()
            subprocess.run(["ssh-add", "-d", pub_file.name],
                           check=False, capture_output=True)
        finally:
            os.unlink(pub_file.name)


@contextlib.contextmanager
def mac_env_unset(var: str) -> Iterator[None]:
    """Temporarily unset a variable in the Mac user's launchd env.

    Used by the agent-missing test to make Mac's ssh-agent unreachable
    for the duration of a single `ssh` attempt without disturbing the
    user's shell env or the agent itself."""
    prev = subprocess.run(
        ["launchctl", "getenv", var],
        capture_output=True, text=True, timeout=5,
    ).stdout.strip()
    subprocess.run(["launchctl", "unsetenv", var],
                   check=False, capture_output=True, timeout=5)
    try:
        yield
    finally:
        if prev:
            subprocess.run(["launchctl", "setenv", var, prev],
                           check=False, capture_output=True, timeout=5)
