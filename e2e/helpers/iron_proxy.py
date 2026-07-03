"""Helpers for iron-proxy contract tests.

Spawns iron-proxy in a subprocess with a fresh config, yielding the
Popen handle. Cleans up the subprocess on exit.

DESIGN NOTE — config on disk, not stdin:
    iron-proxy v0.45.0 only accepts `-config path/to/file.yaml`; there
    is no stdin / `--config -` support. The DESIGN GOAL of "config never
    touches disk" cannot be satisfied with v0.45.0. We write a temp YAML
    file for the duration of the context-manager and delete it on exit.
    Revisit if a future iron-proxy release adds stdin support.
"""
from __future__ import annotations

import contextlib
import os
import socket
import subprocess
import tempfile
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Iterator

import yaml


def _free_port() -> int:
    """Return an ephemeral free TCP port."""
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


@dataclass
class IronProxyConfig:
    """Config blob passed to iron-proxy.

    Field names and nesting match iron-proxy v0.45.0's YAML schema
    (see iron-proxy.example.yaml and README §Configuration).

    Key schema facts:
      - dns.listen         ":PORT" or "HOST:PORT"
      - dns.proxy_ip       IP string; not required when dns.enabled=false
      - proxy.http_listen  ":PORT" or "HOST:PORT"
      - proxy.https_listen ":PORT" or "HOST:PORT"
      - tls.ca_cert        path to CA certificate file
      - tls.ca_key         path to CA private key file
      - transforms         list of {name, config} dicts; allowlist domains go
                           under transforms[{name:"allowlist"}].config.domains;
                           secret substitution entries go under
                           transforms[{name:"secrets"}].config.secrets
      - secret_tokens      dict mapping opaque token (proxy_value) to the env
                           var name that holds the real value; env var values
                           must be provided via spawn()'s env= parameter
    """
    http_listen: str
    https_listen: str
    ca_cert_path: str
    ca_key_path: str
    # DNS is disabled by default in tests to avoid requiring port 53 or root.
    # When dns_enabled=True, dns_listen and dns_proxy_ip must be set.
    dns_enabled: bool = False
    dns_listen: str = ""
    dns_proxy_ip: str = ""
    allow_domains: list[str] = field(default_factory=list)
    # Maps opaque token (proxy_value, e.g. "__DEVM_SECRET_FOO__") to the
    # env var name iron-proxy reads the real value from (e.g. "DEVM_SECRET_FOO").
    # Pass the actual secret values via spawn(env={...}).
    secret_tokens: dict[str, str] = field(default_factory=dict)
    # Maps secret token (proxy_value) → list of hosts the secret may inject
    # for. Absent key ⇒ defaults to ["*"] (inject for any host); explicit
    # [] ⇒ no rules (never injects).
    secret_hosts: dict[str, list[str]] = field(default_factory=dict)

    def to_yaml_dict(self) -> dict:
        cfg: dict = {
            "dns": {"enabled": self.dns_enabled},
            "proxy": {
                "http_listen": self.http_listen,
                "https_listen": self.https_listen,
                # Allow loopback upstream so the test can connect to
                # 127.0.0.1 through the proxy in later tests. Override the
                # default deny that blocks 127.0.0.0/8.
                "upstream_deny_cidrs": [],
            },
            "tls": {
                "ca_cert": self.ca_cert_path,
                "ca_key": self.ca_key_path,
            },
            # Metrics on an ephemeral port so parallel contract tests (or a
            # concurrent devm iron-proxy) don't collide on iron-proxy's
            # default :9090.
            "metrics": {"listen": ":0"},
        }
        if self.dns_enabled:
            cfg["dns"]["listen"] = self.dns_listen
            cfg["dns"]["proxy_ip"] = self.dns_proxy_ip

        transforms: list = []

        if self.allow_domains:
            transforms.append({
                "name": "allowlist",
                "config": {"domains": self.allow_domains},
            })

        if self.secret_tokens:
            entries = []
            for token, env_var in self.secret_tokens.items():
                hosts = self.secret_hosts.get(token, ["*"])
                entries.append({
                    "source": {"type": "env", "var": env_var},
                    "replace": {
                        "proxy_value": token,
                        "match_headers": [],  # [] = all headers
                    },
                    "rules": [{"host": h} for h in hosts],
                })
            transforms.append({
                "name": "secrets",
                "config": {"secrets": entries},
            })

        if transforms:
            cfg["transforms"] = transforms

        return cfg


def _binary_path() -> str:
    here = Path(__file__).resolve().parent.parent.parent  # repo root
    candidate = here / "bin" / "iron-proxy"
    if candidate.exists():
        return str(candidate)
    raise RuntimeError(
        f"iron-proxy not found at {candidate} — run `just fetch-iron-proxy`"
    )


@contextlib.contextmanager
def spawn(
    config: IronProxyConfig,
    timeout: float = 10.0,
    env: dict[str, str] | None = None,
) -> Iterator[subprocess.Popen]:
    """Spawn iron-proxy with the given config. Yields the Popen.

    iron-proxy v0.45.0 requires a YAML config file on disk (no stdin).
    We write a temp file, start the process, wait for the HTTP port to
    bind, yield, then terminate and delete the temp file.

    env: optional extra env vars merged into os.environ for the
         iron-proxy process. Use this to supply real secret values for
         the secrets transform (the YAML config only names the env var,
         never the actual value).
    """
    cfg_dict = config.to_yaml_dict()
    cfg_yaml = yaml.dump(cfg_dict)

    with tempfile.NamedTemporaryFile(
        suffix=".yaml", mode="w", prefix="iron-proxy-", delete=False
    ) as f:
        f.write(cfg_yaml)
        cfg_path = f.name

    proc_env = {**os.environ, **(env or {})}

    try:
        proc = subprocess.Popen(
            [_binary_path(), "-config", cfg_path],
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            env=proc_env,
        )
        try:
            # Wait for the HTTP listen port to bind.
            deadline = time.monotonic() + timeout
            host_port = config.http_listen.lstrip(":")
            if ":" in host_port:
                host, port_str = host_port.rsplit(":", 1)
            else:
                host, port_str = "127.0.0.1", host_port
            port = int(port_str)

            while time.monotonic() < deadline:
                if proc.poll() is not None:
                    out, err = proc.communicate()
                    raise RuntimeError(
                        f"iron-proxy exited early (rc={proc.returncode})\n"
                        f"stdout: {out.decode(errors='replace')}\n"
                        f"stderr: {err.decode(errors='replace')}"
                    )
                try:
                    with socket.create_connection((host, port), timeout=0.5):
                        break
                except OSError:
                    time.sleep(0.1)
            else:
                proc.terminate()
                out, err = proc.communicate(timeout=5)
                raise RuntimeError(
                    f"iron-proxy never bound {config.http_listen}\n"
                    f"stdout: {out.decode(errors='replace')}\n"
                    f"stderr: {err.decode(errors='replace')}"
                )

            try:
                yield proc
            finally:
                proc.terminate()
                try:
                    proc.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    proc.kill()
        except Exception:
            proc.terminate()
            proc.wait(timeout=5)
            raise
    finally:
        Path(cfg_path).unlink(missing_ok=True)


def free_ports(n: int = 2) -> list[int]:
    return [_free_port() for _ in range(n)]
