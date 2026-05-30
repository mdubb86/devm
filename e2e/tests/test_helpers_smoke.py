"""Offline unit tests for helpers (no sbx, no devm)."""
import os
import tempfile
from pathlib import Path

import yaml

from helpers import registry
from helpers.workspace import Workspace


# --- registry ---

def test_registry_append_and_remove(tmp_path, monkeypatch):
    reg = tmp_path / "reg"
    monkeypatch.setenv("E2E_REGISTRY", str(reg))

    registry.append("sandbox", "e2e-foo-1234")
    registry.append("workspace", "/tmp/devm-e2e-foo-1234")
    registry.append("policy", "x.example.invalid")

    lines = reg.read_text().splitlines()
    assert "sandbox\te2e-foo-1234" in lines
    assert "workspace\t/tmp/devm-e2e-foo-1234" in lines
    assert "policy\tx.example.invalid" in lines

    registry.remove("sandbox", "e2e-foo-1234")
    remaining = reg.read_text().splitlines()
    assert "sandbox\te2e-foo-1234" not in remaining
    assert len(remaining) == 2


def test_registry_noop_when_env_unset(tmp_path, monkeypatch):
    monkeypatch.delenv("E2E_REGISTRY", raising=False)
    # Must not raise even though there's no registry file.
    registry.append("sandbox", "whatever")
    registry.remove("sandbox", "whatever")


# --- workspace ---

def test_workspace_write_minimal_devmyaml(tmp_path):
    ws = Workspace(tmp_path, slug="example", sandbox_name="e2e-example-1234")
    ws.write_devmyaml()
    cfg = yaml.safe_load((tmp_path / "devm.yaml").read_text())
    assert cfg["project"]["id"] == "example"
    assert cfg["project"]["sandbox_name"] == "e2e-example-1234"
    assert cfg["project"]["port_offset"] == 51000


def test_workspace_write_with_services_install(tmp_path):
    ws = Workspace(tmp_path, slug="x", sandbox_name="e2e-x-aaaa")
    ws.write_devmyaml(
        install=["touch /tmp/m"],
        services={"api": {"canonical": 8080}},
    )
    cfg = yaml.safe_load((tmp_path / "devm.yaml").read_text())
    assert cfg["install"] == ["touch /tmp/m"]
    assert cfg["services"]["api"]["canonical"] == 8080


def test_workspace_patch_devmyaml(tmp_path):
    ws = Workspace(tmp_path, slug="x", sandbox_name="e2e-x-aaaa")
    ws.write_devmyaml(install=["touch /tmp/a"])
    ws.patch_devmyaml(install=["touch /tmp/b"])
    cfg = yaml.safe_load((tmp_path / "devm.yaml").read_text())
    assert cfg["install"] == ["touch /tmp/b"]
