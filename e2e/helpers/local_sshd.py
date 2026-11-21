"""Per-test fixture sshd on macOS loopback.

Spins up `/usr/sbin/sshd` on an ephemeral loopback port against a
per-test tempdir (host keypair, client keypair, authorized_keys,
sshd_config, known_hosts). Writes a `Host <alias>` block under
`~/.ssh/config.d/devm-e2e-<alias>.conf` that points ssh at the
tempdir's known_hosts (never the user's). Loads the client key into
Mac's ssh-agent on enter and removes it on exit. Idempotent for all
on-disk state via the yielded alias; the `~/.ssh/config.d/` include
is primed once per session by the conftest fixture.

The fixture is a real external sshd that exists BEFORE a project
starts — unlike the inbound-SSH `devm-<vm>` self-loopback, whose
Mac-side ssh_config block is written only after `/vm/start`
returns, which creates a cold-start chicken-and-egg for
CheckKnownHosts.
"""
from __future__ import annotations

import contextlib
import os
import pwd
import secrets
import shutil
import signal
import socket
import subprocess
import tempfile
import time
from pathlib import Path
from typing import Iterator


_SSHD_BIN = "/usr/sbin/sshd"
_READY_TIMEOUT = 5.0


def _current_user() -> str:
    return os.getenv("USER") or pwd.getpwuid(os.getuid()).pw_name


def _free_port() -> int:
    """Bind to :0, read the port, close. Racy — the port may be
    claimed between close and sshd's own bind. Teardown retries once."""
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    try:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]
    finally:
        s.close()


def _extract_host_pub(pub_path: Path) -> str:
    """Return `ssh-ed25519 <blob>` for the host key. ssh-keygen pub
    files are `<type> <blob> <comment>`; strip the trailing comment
    so the known_hosts line only carries the cryptographic material."""
    parts = pub_path.read_text().strip().split()
    if len(parts) < 2:
        raise AssertionError(f"malformed host pubkey: {pub_path.read_text()!r}")
    return f"{parts[0]} {parts[1]}"


def _spawn_sshd(tmp: Path, port: int) -> subprocess.Popen:
    """Spawn sshd in the foreground. -D keeps it in the foreground,
    -e sends logs to stderr so we can capture them to a file."""
    log = open(tmp / "sshd.stderr.log", "wb")
    return subprocess.Popen(
        [_SSHD_BIN, "-D", "-e",
         "-f", str(tmp / "sshd_config"),
         "-p", str(port)],
        stdout=subprocess.DEVNULL, stderr=log,
    )


def _wait_ready(proc: subprocess.Popen, port: int, log_path: Path) -> None:
    deadline = time.monotonic() + _READY_TIMEOUT
    while time.monotonic() < deadline:
        if proc.poll() is not None:
            log = log_path.read_text(errors="replace") if log_path.exists() else ""
            raise AssertionError(
                f"sshd exited before port came up (returncode={proc.returncode}):\n"
                f"{log}"
            )
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=0.1):
                return
        except OSError:
            time.sleep(0.1)
    log = log_path.read_text(errors="replace") if log_path.exists() else ""
    raise AssertionError(
        f"sshd never listened on 127.0.0.1:{port} within {_READY_TIMEOUT}s:\n{log}"
    )


def _write_sshd_config(tmp: Path, user: str) -> None:
    (tmp / "sshd_config").write_text(
        # Port is set via -p; the placeholder here is a no-op so a
        # casual reader sees the port lives in the launch args.
        "Port 0\n"
        "ListenAddress 127.0.0.1\n"
        f"HostKey {tmp}/host_key\n"
        f"PidFile {tmp}/sshd.pid\n"
        "AuthenticationMethods publickey\n"
        "PubkeyAuthentication yes\n"
        "PasswordAuthentication no\n"
        "KbdInteractiveAuthentication no\n"
        "ChallengeResponseAuthentication no\n"
        "PermitRootLogin no\n"
        "UsePAM no\n"
        f"AllowUsers {user}\n"
        f"AuthorizedKeysFile {tmp}/authorized_keys\n"
        "StrictModes no\n"
        "LogLevel VERBOSE\n"
        "AcceptEnv LANG LC_*\n"
        "Subsystem sftp internal-sftp\n"
    )


def _write_ssh_config(alias: str, tmp: Path, port: int, user: str) -> Path:
    cfg_dir = Path.home() / ".ssh" / "config.d"
    cfg_dir.mkdir(parents=True, exist_ok=True)
    cfg_dir.chmod(0o700)
    conf = cfg_dir / f"devm-e2e-{alias}.conf"
    conf.write_text(
        f"Host {alias}\n"
        "    HostName 127.0.0.1\n"
        f"    Port {port}\n"
        f"    User {user}\n"
        f"    IdentityFile {tmp}/client_key\n"
        "    IdentitiesOnly yes\n"
        f"    HostKeyAlias {alias}\n"
        "    StrictHostKeyChecking yes\n"
        f"    UserKnownHostsFile {tmp}/known_hosts\n"
    )
    conf.chmod(0o600)
    return conf


def _kill_sshd(proc: subprocess.Popen, pid_file: Path) -> None:
    pid = None
    try:
        pid = int(pid_file.read_text().strip())
    except (FileNotFoundError, ValueError):
        pid = proc.pid
    try:
        os.kill(pid, signal.SIGTERM)
    except ProcessLookupError:
        return
    deadline = time.monotonic() + 2.0
    while time.monotonic() < deadline:
        if proc.poll() is not None:
            return
        time.sleep(0.05)
    try:
        os.kill(pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    try:
        proc.wait(timeout=2)
    except subprocess.TimeoutExpired:
        pass


@contextlib.contextmanager
def local_sshd_target(label: str | None = None) -> Iterator[str]:
    """Spin up a fixture sshd on 127.0.0.1:<ephemeral> and yield the
    ssh alias pointing at it. Alias matches `sshAliasPattern`
    (`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,252}$`).

    All state (keys, sshd_config, known_hosts, ssh_config fragment)
    is torn down on exit, including the ssh-agent identity. The
    daemon's process is killed via SIGTERM then SIGKILL after 2s.
    """
    alias = label or f"devm-e2e-sshd-{secrets.token_hex(3)}"
    user = _current_user()
    tmp = Path(tempfile.mkdtemp(prefix="devm-e2e-sshd-"))
    tmp.chmod(0o700)
    proc: subprocess.Popen | None = None
    conf_path: Path | None = None
    client_key_pub = tmp / "client_key.pub"
    try:
        for name, comment in (("host_key", "devm-e2e-host"),
                              ("client_key", "devm-e2e-client")):
            subprocess.run(
                ["ssh-keygen", "-t", "ed25519", "-f", str(tmp / name),
                 "-N", "", "-C", comment],
                check=True, capture_output=True,
            )
        (tmp / "authorized_keys").write_text(
            (tmp / "client_key.pub").read_text()
        )
        for f in ("host_key", "client_key", "authorized_keys"):
            (tmp / f).chmod(0o600)

        _write_sshd_config(tmp, user)

        # A bind-then-close race with another process on the chosen
        # port is harmless — sshd's own bind will fail and we spawn
        # again on a fresh port.
        last_err: Exception | None = None
        for _ in range(2):
            port = _free_port()
            proc = _spawn_sshd(tmp, port)
            try:
                _wait_ready(proc, port, tmp / "sshd.stderr.log")
                last_err = None
                break
            except AssertionError as e:
                last_err = e
                _kill_sshd(proc, tmp / "sshd.pid")
                proc = None
        if proc is None:
            raise last_err or AssertionError("sshd never came up")

        host_pub = _extract_host_pub(tmp / "host_key.pub")
        (tmp / "known_hosts").write_text(f"{alias} {host_pub}\n")

        conf_path = _write_ssh_config(alias, tmp, port, user)

        subprocess.run(["ssh-add", str(tmp / "client_key")],
                       check=True, capture_output=True)

        yield alias
    finally:
        # ssh-add -d takes the pubkey path.
        if client_key_pub.exists():
            subprocess.run(["ssh-add", "-d", str(client_key_pub)],
                           check=False, capture_output=True)
        if proc is not None:
            _kill_sshd(proc, tmp / "sshd.pid")
        if conf_path is not None:
            conf_path.unlink(missing_ok=True)
        shutil.rmtree(tmp, ignore_errors=True)
