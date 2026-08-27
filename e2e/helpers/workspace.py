"""Workspace helper: tempdir + devm.yaml builder/patcher.

A test workspace is a directory containing a freshly-rendered
devm.yaml. The Workspace knows how to write a minimal config and
how to patch named sections without breaking YAML.
"""
from __future__ import annotations
import subprocess
from pathlib import Path
from typing import Any

import yaml


class Workspace:
    def __init__(self, path: Path, slug: str, vm_name: str, port_offset: int = 51000):
        self.path = Path(path)
        self.slug = slug
        self.vm_name = vm_name
        self.port_offset = port_offset
        self._bare_repo_cache: str | None = None

    @property
    def devmyaml_path(self) -> Path:
        return self.path / "devm.yaml"

    def bare_repo_url(self) -> str:
        """Create (once) a local bare git repo for this test and return its
        file:// URL. Hermetic and fast — no network, no real remote.

        `work`/`bare` are siblings of self.path, named off its unique
        leaf (not off self.path.parent, which is the shared OS temp
        root every workspace's path lives under -- suffixing that
        directly would collide across every test in the suite)."""
        if self._bare_repo_cache is not None:
            return self._bare_repo_cache
        work = self.path.parent / f"{self.path.name}-repo-work"
        work.mkdir()
        subprocess.run(["git", "-C", str(work), "init", "-q"], check=True)
        (work / "README.md").write_text("bare\n")
        subprocess.run(["git", "-C", str(work), "add", "."], check=True)
        subprocess.run(
            ["git", "-C", str(work), "-c", "user.email=e2e@e2e", "-c", "user.name=e2e",
             "commit", "-q", "-m", "init"],
            check=True,
        )
        bare = self.path.parent / f"{self.path.name}-repo.git"
        subprocess.run(["git", "clone", "--bare", "-q", str(work), str(bare)], check=True)
        self._bare_repo_cache = f"file://{bare}"
        return self._bare_repo_cache

    def volume_path(self, name: str | None = None) -> Path:
        """Return the Mac-side volume storage path for a project volume.

        name=None -> primary (the daemon derives the primary volume's
        name from the basename of the Mac cwd, i.e. this workspace dir).
        Hardcoded to the devm-e2e identity's RuntimeDir to match the
        daemon under test (see internal/identity.E2E.RuntimeDir()).
        """
        if name is None:
            name = self.path.name
        return Path.home() / "Library/Application Support/devm-e2e/volumes" / self.vm_name / name

    def write_devmyaml(self, *, no_repo: bool = False, **sections: Any) -> None:
        """Write a fresh devm.yaml. Extra sections (install, services, env,
        network) are merged into the project skeleton. A `repos:` map with
        a single "main" entry is auto-injected (pointing at a hermetic
        local bare repo, which shells out to git) unless the caller opts
        out: pass an explicit `repos={...}` section to use verbatim, pass
        `repo=False` or `no_repo=True` to omit the `repos:` block entirely
        without ever calling `bare_repo_url()`.

        The singular `repo=...` kwarg (the pre-Phase-B shape) is no
        longer accepted -- Config.Repos is a map now, and the daemon's
        KnownFields(true) decode rejects a stray `repo:` key outright.
        Callers must pass `repos={"main": {...}}` (or another id)
        instead."""
        if "repo" in sections and sections["repo"] is not False:
            raise ValueError(
                "write_devmyaml(repo=...) is no longer supported: devm.yaml "
                "now uses a `repos:` map. Pass repos={'main': {...}} instead "
                "(or repo=False / no_repo=True to omit repos entirely)."
            )
        if sections.get("repo") is False:
            no_repo = True
            del sections["repo"]
        if not no_repo and "repos" not in sections:
            sections["repos"] = {
                "main": {
                    "url": self.bare_repo_url(),
                    "secret": "e2e_default",
                },
            }
        cfg: dict[str, Any] = {
            "project": {
                "name": self.vm_name,
            },
        }
        for k, v in sections.items():
            cfg[k] = v
        self.devmyaml_path.write_text(yaml.safe_dump(cfg, sort_keys=False))

    def patch_devmyaml(self, **sections: Any) -> None:
        """Update named top-level sections in the existing devm.yaml."""
        cfg = yaml.safe_load(self.devmyaml_path.read_text()) or {}
        for k, v in sections.items():
            cfg[k] = v
        self.devmyaml_path.write_text(yaml.safe_dump(cfg, sort_keys=False))

    def proxy_log_path(self) -> Path:
        """Mac-side path to this project's iron-proxy audit log under the
        devm-e2e identity's LogDir (~/Library/Logs/devm-e2e/). Filename is
        `<project-name>-proxy.log` — supervisor.Key{ProjectID, RoleProxy}
        keys the log file on project.name, which is this workspace's
        vm_name (see internal/identity.Config.LogDir,
        internal/supervisor.New's log naming)."""
        return Path.home() / "Library" / "Logs" / "devm-e2e" / f"{self.vm_name}-proxy.log"

    def read_proxy_log(self) -> list[str]:
        """Return the iron-proxy audit log's lines for this project, or
        an empty list if iron-proxy hasn't written one yet."""
        path = self.proxy_log_path()
        if not path.exists():
            return []
        return path.read_text(errors="replace").splitlines(keepends=True)

    def add_systemd_service(self, name: str, exec: list[str], restart: str = "always", **extra) -> None:
        """Add (or replace) a systemd service block under services.<name>.

        Use this from tests that need a "service that stays alive" pattern —
        cleaner than threading the full services dict through write_devmyaml
        on every call.
        """
        import yaml
        cfg = yaml.safe_load(self.devmyaml_path.read_text()) or {}
        services = cfg.setdefault("services", {})
        services[name] = {"exec": exec, "restart": restart, **extra}
        self.devmyaml_path.write_text(yaml.safe_dump(cfg, sort_keys=False))
