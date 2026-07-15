"""Workspace helper: tempdir + devm.yaml builder/patcher.

A test workspace is a directory containing a freshly-rendered
devm.yaml. The Workspace knows how to write a minimal config and
how to patch named sections without breaking YAML.
"""
from __future__ import annotations
from pathlib import Path
from typing import Any

import yaml


class Workspace:
    def __init__(self, path: Path, slug: str, vm_name: str, port_offset: int = 51000):
        self.path = Path(path)
        self.slug = slug
        self.vm_name = vm_name
        self.port_offset = port_offset

    @property
    def devmyaml_path(self) -> Path:
        return self.path / "devm.yaml"

    def write_devmyaml(self, **sections: Any) -> None:
        """Write a fresh devm.yaml. Extra sections (install, services, env,
        network) are merged into the project skeleton."""
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
