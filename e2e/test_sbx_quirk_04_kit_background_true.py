"""sbx-quirk 04: pins the upstream sbx behavior that a startup step
declared with the kit's own `background: true` field dies at ~5s,
REGARDLESS of `nohup` wrapping or anchor state.

This is what motivated devm's render translation: instead of
emitting `background: true` in the sbx kit YAML, devm renders the
step as a FOREGROUND step whose command is wrapped at the shell
level with `nohup ... &`. The community kits (e.g.
docker/sbx-kits-contrib/code-server) do the same.

If a future sbx release fixes the `background: true` kit-flag
semantics, this test fails — and we can simplify devm's render
back to using the flag directly.
"""
from __future__ import annotations
import os
import signal
import subprocess
import tempfile
import textwrap
import time

import pytest

from helpers import sbx


# A kit with one `background: true` startup step. We use a heartbeat
# pattern so we can measure lifetime; the upstream quirk should kill
# the process before lifetime > ~10s regardless of what we do.
KIT_SPEC = textwrap.dedent("""\
    schemaVersion: "1"
    kind: agent
    name: bgtrue
    displayName: kit background-true probe
    agent:
      image: docker/sandbox-templates:shell
      aiFilename: CLAUDE.md
      entrypoint:
        run: ["sh", "-c", "exec sleep infinity </dev/null"]
    environment:
      variables:
        IS_SANDBOX: "1"
    commands:
      startup:
        - command: ['sh', '-c', 'date +%s.%N > /tmp/daemon-start; while true; do date +%s.%N >> /tmp/daemon-trail; sleep 0.1; done']
          background: true
          user: "1000"
          description: 'kit background:true probe (no nohup wrap)'
""")


def _read_lifetime(name: str):
    r = subprocess.run(
        ["sbx", "exec", name, "sh", "-c",
         "cat /tmp/daemon-start 2>/dev/null; echo ===; "
         "tail -1 /tmp/daemon-trail 2>/dev/null; echo ===; "
         "pgrep -af 'while true.*daemon-trail' | grep -v pgrep && echo ALIVE || echo DEAD"],
        capture_output=True, timeout=10,
    )
    if r.returncode != 0:
        return None
    parts = r.stdout.decode().split("===")
    if len(parts) < 3:
        return None
    try:
        start = float(parts[0].strip())
        last = float(parts[1].strip())
        alive = "ALIVE" in parts[2]
        return (start, last, alive, last - start)
    except ValueError:
        return None


@pytest.mark.timeout(120)
def test_kit_background_true_kills_at_5s(sandbox_name):
    """A step with kit-level `background: true` dies at ~5s.
    Asserted as a quirk: passes if upstream still kills, fails if
    upstream is fixed (so we can drop the foreground+nohup workaround)."""
    workspace = tempfile.mkdtemp(prefix="quirk-bgtrue-ws-")
    kit_dir = tempfile.mkdtemp(prefix="quirk-bgtrue-kit-")
    with open(os.path.join(kit_dir, "spec.yaml"), "w") as f:
        f.write(KIT_SPEC)

    anchor = subprocess.Popen(
        ["sbx", "run", "--kit", kit_dir, "--name", sandbox_name,
         "bgtrue", workspace],
        stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=None,
    )
    try:
        # Wait running + exec-ready.
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            if sbx.sandbox_state(sandbox_name) == "running":
                break
            time.sleep(0.25)
        else:
            pytest.fail("sandbox never running")
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            if subprocess.run(["sbx", "exec", sandbox_name, "true"],
                              capture_output=True, timeout=5).returncode == 0:
                break
            time.sleep(0.25)
        else:
            pytest.fail("sandbox not exec-ready")

        # Wait well past the 5s window.
        time.sleep(15)
        result = _read_lifetime(sandbox_name)
        assert result is not None, "could not read daemon trail"
        start, last, alive, lifetime = result
        print(f"\n  start={start:.3f} last={last:.3f} alive={alive} "
              f"lifetime={lifetime:.2f}s\n", flush=True)

        # Locked behavior (sbx 0.31+): kit-level `background: true` no
        # longer kills the step at ~5s — daemon stays alive. Before
        # 0.31 this assertion was `assert not alive` (the historical
        # quirk). If sbx ever REGRESSES, this test will fail loud and
        # we'll know to bring back the foreground+nohup workaround in
        # internal/render/spec.go.
        assert alive, (
            f"`background: true` step DIED before 15s elapsed — sbx may "
            f"have re-introduced the 5s kill timer. Restore the foreground+"
            f"nohup workaround in internal/render/spec.go."
        )
        assert lifetime >= 14, (
            f"daemon trail only spans {lifetime:.2f}s; the step likely "
            f"died early. See assertion above."
        )
    finally:
        if anchor.poll() is None:
            anchor.kill()
            try:
                anchor.wait(timeout=3)
            except Exception:
                pass
        sbx.sandbox_rm(sandbox_name)
        import shutil
        shutil.rmtree(workspace, ignore_errors=True)
        shutil.rmtree(kit_dir, ignore_errors=True)
