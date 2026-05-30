"""Offline unit tests for helpers (no sbx, no devm)."""
import os
import tempfile

from helpers import registry


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
