"""softnet control-socket + $PATH lookup for e2e tests that boot a VM
directly via `tart run` (bypassing the daemon's own `/vm/start` spawn
path entirely -- see e.g. test_91_gate_adopt_in_place.py's raw-boot
setup step, which reproduces the boot-integrity gate's locked/inert
shape by booting outside devm on purpose).

`tart run --net-softnet` resolves a binary literally named `softnet` on
the child process's $PATH and execs it; the daemon makes that resolve
to itself via a symlink at `<runtime_dir>/softnet-bin/softnet` (see
internal/serviceapi/softnet_paths.go: ensureSoftnetSymlink), and tells
the freshly-spawned softnet where to bind its control socket via
$SOFTNET_CONTROL_SOCK (internal/serviceapi/vm.go's /vm/start handler).
Both are deterministic functions of (runtime_dir, project_id) -- see
softnet_paths.go: SoftnetControlSock -- so a raw `tart run` invocation
from Python can reproduce them exactly.

This matters beyond just getting softnet to run at all: the already-
running e2e daemon repopulates its in-memory projectID -> control-sock
map (softnetState) from this SAME deterministic path whenever
/vm/apply-iron-proxy runs (the adopt-in-place path -- see
apply_iron_proxy.go's softnetState.put). If a raw `tart run` here binds
its control socket somewhere else, that repopulated map entry points at
nothing, and /vm/apply-egress-enforcement 412s.
"""
from __future__ import annotations

import hashlib
import os

# Must match identity.E2E.Name ("devm-e2e") -- e2e/helpers/pool.py hardcodes
# the same runtime dir for the same reason (no Python-side identity.Load()).
_RUNTIME_DIR = os.path.expanduser("~/Library/Application Support/devm-e2e")


def softnet_bin_dir() -> str:
    """Directory containing the `softnet` symlink to the devm-e2e binary.
    Created by the daemon's own /vm/start (ensureSoftnetSymlink) the
    first time any project cold-starts this daemon lifetime -- callers
    that boot a VM raw only after an earlier cold-start already ran
    (test_91's pattern: cold-start, stop, THEN raw tart run) can rely on
    it already existing."""
    return os.path.join(_RUNTIME_DIR, "softnet-bin")


def softnet_control_sock(project_id: str) -> str:
    """Reproduce internal/serviceapi/softnet_paths.go's
    SoftnetControlSock(cfg, projectID): sha256(runtime_dir + "\\x00" +
    project_id), first 20 hex chars, under /tmp/devm-softnet-<uid>/."""
    digest = hashlib.sha256(
        (_RUNTIME_DIR + "\x00" + project_id).encode()
    ).hexdigest()[:20]
    return f"/tmp/devm-softnet-{os.getuid()}/{digest}.sock"


def env_with_softnet(project_id: str, base_env: dict | None = None) -> dict:
    """Return an env dict (starting from base_env or a copy of the
    current process env, mirroring vm.go's `env := os.Environ()` full-
    inheritance-then-augment approach) with softnet_bin_dir() prepended
    to $PATH and $SOFTNET_CONTROL_SOCK set to project_id's deterministic
    control socket -- the exact env a raw `tart run --net-softnet
    <project_id>` needs to reproduce what the daemon's own /vm/start
    would have set up."""
    env = dict(base_env) if base_env is not None else dict(os.environ)
    bin_dir = softnet_bin_dir()
    env["PATH"] = bin_dir + os.pathsep + env.get("PATH", "")
    env["SOFTNET_CONTROL_SOCK"] = softnet_control_sock(project_id)
    return env
