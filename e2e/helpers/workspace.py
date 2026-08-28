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


# github.com/octocat/Hello-World is github's canonical demo repo: public,
# tiny (a single README-ish file), and stable. Tests that need to observe
# a real remote clone through iron-proxy point here. Iron-proxy substitutes
# the placeholder secret on the wire; github ignores the auth for public
# reads, so no real PAT is needed.
E2E_FIXTURE_REPO_URL = "https://github.com/octocat/Hello-World.git"


class Workspace:
    def __init__(self, path: Path, slug: str, vm_name: str, port_offset: int = 51000):
        self.path = Path(path)
        self.slug = slug
        self.vm_name = vm_name
        self.port_offset = port_offset

    @property
    def devmyaml_path(self) -> Path:
        return self.path / "devm.yaml"

    def bare_repo_url(self) -> str:
        """Return the URL of the shared public remote every test's default
        `repos.main` points at. Guest clones it through iron-proxy's
        transparent :443 intercept.
        """
        return E2E_FIXTURE_REPO_URL

    def bare_repo_label(self) -> str:
        """Return schema.BareCloneName(bare_repo_url()) — the label devm
        derives for the default `repos.main` entry. Kept as a helper so
        tests don't hardcode the shape (previous fixture used a
        `<slug>-repo.git` URL, giving label `<slug>-repo`; switching to
        github.com/octocat/Hello-World.git changed the label to
        `Hello-World`, and every test that had computed the label from
        `path.name + "-repo"` silently broke). Read this instead."""
        return "Hello-World"

    def teardown(self) -> None:
        """Present for symmetry with fixtures that manage per-workspace
        resources; no-op today.
        """
        return None

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
            # bare_repo_url() is github's public octocat/Hello-World repo,
            # cloneable without auth. Omitting `secret:` tells the daemon
            # not to inject an http.extraheader — github rejects a bogus
            # Basic auth token even for public reads.
            sections["repos"] = {
                "main": {
                    "url": self.bare_repo_url(),
                    "primary": True,
                },
            }
        cfg: dict[str, Any] = {
            "project": {
                "name": self.vm_name,
            },
        }
        # git is a hard requirement in-guest for mutagen cold-start clone.
        # Base image doesn't ship it yet; declare here so RunOpen apt-installs
        # it before SetupPhase runs. Remove once the base image bakes git in.
        if not no_repo and "packages" not in sections:
            cfg["packages"] = ["git"]
        # bare_repo_url() points at github.com's canonical demo repo; guest
        # clones through iron-proxy's transparent :443 intercept. Callers
        # passing their own `network` block are responsible for including
        # `github.com` if they want the default `repos.main` to clone.
        if not no_repo and "network" not in sections:
            cfg["network"] = {"allow": ["github.com"]}
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
