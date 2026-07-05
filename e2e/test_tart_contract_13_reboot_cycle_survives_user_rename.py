"""Pin: after a systemd one-shot (fired during a fresh boot cycle) renames
the guest user + updates tart-guest-agent.service's User= line, the SECOND
`tart run` on the same VM must produce a working guest-agent socket that
`tart exec` can reach.

Devm's Go builder (`internal/image/builder.go`) drives exactly this cycle
to bake the admin -> devm rename into `devm-base`:

  1. tart run --no-graphics devm-base  (as admin)
  2. tart exec -i devm-base sudo bash -s < provision-base.sh
       — installs the rename one-shot + updates tart-guest-agent
         unit's User=admin -> User=devm
  3. tart exec devm-base sudo systemctl poweroff
  4. tart-run child exits (cmd.Wait returns)
  5. tart run --no-graphics devm-base  (fresh; one-shot fires on this boot)
  6. tart exec devm-base id -un        <-- must return "devm"

Empirically, step (6) has been failing with `GRPCConnectionPoolError` even
though the rename itself succeeded when observed via manual `tart run`.
This test isolates the boot cycle from devm's builder — reproduces it
against the raw cirruslabs template with a Mac-side debug mount, so we
can see every step of the rename script AND the post-restart guest state.

Design:
  - Mount `<host tmpdir>:tag=debug` into the guest at /mnt/debug.
  - Install a `detective` bash wrapper that logs {ts, argv, stdout, stderr,
    rc, guest state snapshot} to /mnt/debug/<seq>-<cmd>.log for every
    mutating command in the rename script.
  - Also log guest state BEFORE and AFTER the rename (id, ls /home,
    systemctl status tart-guest-agent, /etc/passwd 1000 entry).
  - Trigger the reboot cycle exactly as the Go builder does.
  - Poll the second boot's guest-agent readiness with a bounded per-attempt
    timeout so a hang surfaces as a failed iteration, not a wall-clock
    timeout.
  - Read /mnt/debug/* on the Mac after the second boot (whether it
    succeeded or not) and assert on the collected evidence.

Failure modes this test can distinguish:
  - Rename script itself failed (a detective log will show non-zero rc).
  - Rename script succeeded but tart-guest-agent didn't restart cleanly
    (systemctl status snapshot will show failed/inactive).
  - tart-guest-agent restarted but tart's outer socket handle is stale
    (guest looks healthy from inside, `tart exec` still fails from outside).
  - Second `tart run` never gets an IP at all (distinct signal from a
    working boot with an unreachable agent).

If the test passes, we've pinned that the boot cycle IS safe under this
scenario, and any failure in devm's own builder is a devm-side bug (not
a tart limit). If it fails, the /mnt/debug logs identify which specific
step of the cycle breaks — no more speculation.
"""
from __future__ import annotations

import secrets
import subprocess
import tempfile
import textwrap
import time
from pathlib import Path

import pytest

from helpers import registry
from helpers.tart import TartSandbox


TEMPLATE = "ghcr.io/cirruslabs/debian:latest"
MOUNTPOINT = "/mnt/debug"


DETECTIVE_WRAPPER = r"""#!/bin/bash
# detective — log every wrapped command's argv, rc, stdout, stderr.
# Called as: detective <label> -- <cmd> [args...]
# Writes to $DETECTIVE_DIR/<NN>-<label>.log and stdout/stderr passthroughs.
set +e
LABEL="$1"; shift
[ "$1" = "--" ] && shift

DIR="${DETECTIVE_DIR:-/mnt/debug}"
NN=$(printf '%03d' "$(cat "$DIR/.seq" 2>/dev/null || echo 0)")
next=$(( ${NN#0*} + 1 ))
echo "$next" > "$DIR/.seq"

LOG="$DIR/$NN-$LABEL.log"
{
  echo "=== ts: $(date -Iseconds) ==="
  echo "=== argv: $* ==="
  echo "=== id: $(id) ==="
} >> "$LOG" 2>&1

STDOUT=$("$@" 2>>"$LOG")
RC=$?
{
  echo "=== rc: $RC ==="
  echo "=== stdout: ==="
  echo "$STDOUT"
} >> "$LOG"
printf '%s' "$STDOUT"
exit "$RC"
"""

# Same rename script content as image/provision-base.sh's devm-rename-user,
# but wrapped in detective calls. The one-shot unit points at this.
DETECTIVE_RENAME = r"""#!/bin/bash
set -e
export DETECTIVE_DIR=/mnt/debug

detective preflight-id -- /usr/bin/id
detective preflight-passwd -- /bin/bash -c 'grep -E "^(admin|devm):" /etc/passwd'
detective preflight-home -- /bin/ls -la /home
detective preflight-guest-agent -- /bin/systemctl is-active tart-guest-agent

# Idempotent guard — matches provision-base.sh.
if id devm >/dev/null 2>&1; then
    echo "already renamed" > /mnt/debug/000-already-renamed.log
    exit 0
fi
if ! id admin >/dev/null 2>&1; then
    echo "no admin user, nothing to do" > /mnt/debug/000-no-admin.log
    exit 0
fi

detective usermod-login -- /usr/sbin/usermod -l devm admin
detective usermod-home -- /usr/sbin/usermod -d /home/devm -m devm
detective groupmod -- /usr/sbin/groupmod -n devm admin

for u in /usr/lib/systemd/system/tart-guest-agent.service /etc/systemd/system/tart-guest-agent.service; do
    if [ -f "$u" ]; then
        detective sed-guest-agent-unit -- /bin/sed -i 's/^User=admin$/User=devm/' "$u"
    fi
done

for f in /etc/sudoers.d/*; do
    if [ -f "$f" ] && /bin/grep -q '\<admin\>' "$f"; then
        detective sed-sudoers -- /bin/sed -i 's/\<admin\>/devm/g' "$f"
    fi
done

detective postflight-id -- /usr/bin/id
detective postflight-passwd -- /bin/bash -c 'grep -E "^(admin|devm):" /etc/passwd'
detective postflight-home -- /bin/ls -la /home
detective postflight-guest-agent-unit -- /bin/cat /etc/systemd/system/tart-guest-agent.service
"""

DETECTIVE_ONESHOT_UNIT = """[Unit]
Description=Contract-13 detective rename (admin -> devm)
DefaultDependencies=no
Before=tart-guest-agent.service
After=local-fs.target
ConditionPathExists=!/var/lib/devm/user-renamed

[Service]
Type=oneshot
ExecStart=/usr/local/bin/devm-rename-user
ExecStartPost=/bin/sh -c "mkdir -p /var/lib/devm && touch /var/lib/devm/user-renamed"
StandardOutput=append:/mnt/debug/999-oneshot-service.log
StandardError=append:/mnt/debug/999-oneshot-service.log
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
"""


@pytest.fixture
def rename_reboot_lab():
    """Set up a raw cirruslabs VM with the detective mount + rename one-shot
    installed. Yields (name, host_debug_dir, tart_run_proc). Caller drives
    the reboot cycle.
    """
    name = f"contract13-{secrets.token_hex(2)}"
    registry.append("sandbox", name)

    debug_ctx = tempfile.TemporaryDirectory(prefix="contract13-debug-")
    host_debug = Path(debug_ctx.name)
    (host_debug / ".seq").write_text("1")

    first_proc = None
    try:
        subprocess.run(["tart", "pull", TEMPLATE], check=True, timeout=300)
        subprocess.run(["tart", "clone", TEMPLATE, name], check=True, timeout=60)

        dir_arg = f"--dir={host_debug}:tag=debug"
        first_proc = subprocess.Popen(
            ["tart", "run", "--no-graphics", dir_arg, name],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        )

        vm = TartSandbox(name=name)
        assert vm.wait_running(timeout=120), f"{name} never running"
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            if vm.exec("true").ok:
                break
            time.sleep(1)
        else:
            raise RuntimeError(f"{name} never exec-ready on first boot")

        # Mount the debug share, install detective + rename script + unit,
        # enable the unit.
        setup = vm.exec_shell(textwrap.dedent(f"""
            set -e
            sudo mkdir -p {MOUNTPOINT}
            sudo mount -t virtiofs debug {MOUNTPOINT}
            sudo chmod 0777 {MOUNTPOINT}
        """))
        assert setup.ok, f"mount debug share failed: {setup.stderr!r}"

        # Write detective + rename script + unit via heredoc-through-exec.
        install = subprocess.run(
            ["tart", "exec", "-i", name, "sudo", "bash", "-c", "cat > /tmp/install.sh && bash /tmp/install.sh"],
            input=textwrap.dedent(f"""
                set -e
                cat > /usr/local/bin/detective <<'DETSCRIPT'
{DETECTIVE_WRAPPER}
DETSCRIPT
                chmod +x /usr/local/bin/detective

                cat > /usr/local/bin/devm-rename-user <<'RENAMESCRIPT'
{DETECTIVE_RENAME}
RENAMESCRIPT
                chmod +x /usr/local/bin/devm-rename-user

                cat > /etc/systemd/system/devm-rename-user.service <<'UNITSCRIPT'
{DETECTIVE_ONESHOT_UNIT}
UNITSCRIPT

                systemctl daemon-reload
                systemctl enable devm-rename-user.service

                # Also stamp the unmounted state so we can see what
                # /etc/fstab looks like on next boot (bind our
                # virtiofs mount so it survives the reboot).
                if ! grep -q '^debug ' /etc/fstab; then
                    echo 'debug {MOUNTPOINT} virtiofs defaults 0 0' >> /etc/fstab
                fi
            """).encode(),
            capture_output=True, timeout=60,
        )
        assert install.returncode == 0, (
            f"install failed: stdout={install.stdout.decode()!r} "
            f"stderr={install.stderr.decode()!r}"
        )

        yield vm, host_debug, first_proc
    finally:
        subprocess.run(["tart", "stop", name], capture_output=True, timeout=30)
        if first_proc:
            try:
                first_proc.wait(timeout=30)
            except subprocess.TimeoutExpired:
                first_proc.kill()
        subprocess.run(["tart", "delete", name], capture_output=True, timeout=10)
        registry.remove("sandbox", name)
        debug_ctx.cleanup()


def _read_debug_logs(debug_dir: Path) -> str:
    """Concatenate all detective logs in order for post-hoc reporting."""
    parts = []
    for f in sorted(debug_dir.glob("*.log")):
        parts.append(f"===== {f.name} =====\n{f.read_text()}\n")
    return "".join(parts) or "(no detective logs written)"


@pytest.mark.contract
@pytest.mark.slow
@pytest.mark.timeout(600)
def test_reboot_cycle_after_user_rename(rename_reboot_lab):
    """The full cycle: poweroff, second `tart run`, exec must reach the new
    tart-guest-agent (now running as `devm`).

    First run of this test on a broken cycle produces the failure evidence
    in-tree. Passing runs pin that the mechanism IS safe against the raw
    cirruslabs template.
    """
    vm, host_debug, first_proc = rename_reboot_lab

    # Step 1: guest poweroff.
    poweroff = vm.exec("sudo", "systemctl", "poweroff", timeout=30)
    # Best-effort — the guest-agent channel often closes mid-shutdown so a
    # nonzero rc here is expected.

    # Step 2: wait for the first tart-run process to exit (this IS the "VM
    # stopped" signal — same design the Go builder uses).
    try:
        first_proc.wait(timeout=60)
    except subprocess.TimeoutExpired:
        first_proc.kill()
        pytest.fail(
            "first tart-run did not exit within 60s of guest poweroff. "
            "This is a distinct failure mode from the guest-agent handshake — "
            "poweroff itself didn't propagate cleanly.\n\n"
            f"debug logs so far:\n{_read_debug_logs(host_debug)}"
        )

    # Step 3: second tart run, from a fresh cold boot. This is where the
    # one-shot fires and tart-guest-agent restarts as `devm`.
    dir_arg = f"--dir={host_debug}:tag=debug"
    second_proc = subprocess.Popen(
        ["tart", "run", "--no-graphics", dir_arg, vm.name],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )

    try:
        # Step 4: bounded readiness — each attempt is its own 3s subprocess
        # so no single hung `tart exec` blocks the whole loop.
        reachable = False
        deadline = time.monotonic() + 180
        attempt = 0
        while time.monotonic() < deadline:
            attempt += 1
            r = subprocess.run(
                ["tart", "exec", vm.name, "true"],
                capture_output=True, timeout=3,
            )
            if r.returncode == 0:
                reachable = True
                break
            time.sleep(1)

        # Step 5: capture identity (or the failure to capture one) BEFORE we
        # tear down, so the assertion messages have real evidence.
        r = subprocess.run(
            ["tart", "exec", vm.name, "id", "-un"],
            capture_output=True, timeout=5,
        )
        identity = r.stdout.decode().strip() if r.returncode == 0 else f"(unreachable, rc={r.returncode}, stderr={r.stderr.decode()!r})"

        # Grab the debug transcript regardless of pass/fail — this is the
        # whole point of the harness.
        debug_transcript = _read_debug_logs(host_debug)

        assert reachable, (
            f"second tart-run's guest-agent socket never accepted a `tart exec` "
            f"call within 180s (across {attempt} attempts, each bounded to 3s). "
            f"identity capture attempt: {identity!r}\n\n"
            f"=== debug transcript ===\n{debug_transcript}"
        )
        assert identity == "devm", (
            f"second boot's identity is {identity!r}, expected 'devm'. "
            f"The rename one-shot either didn't fire, or fired without "
            f"switching tart-guest-agent's User=. "
            f"\n\n=== debug transcript ===\n{debug_transcript}"
        )
    finally:
        subprocess.run(["tart", "stop", vm.name], capture_output=True, timeout=30)
        try:
            second_proc.wait(timeout=30)
        except subprocess.TimeoutExpired:
            second_proc.kill()
