"""Pin: `tart list --format=json` returns the fields Go code parses.

Go's internal/sandbox/tart wrapper unmarshals this JSON. If the field
casing or shape ever changes, the orchestrator breaks silently.
"""
import json
import subprocess

import pytest


@pytest.mark.contract
def test_tart_list_json_shape(inspector_vm):
    r = subprocess.run(
        ["tart", "list", "--format=json"],
        capture_output=True, timeout=10,
    )
    assert r.returncode == 0
    entries = json.loads(r.stdout.decode())
    assert isinstance(entries, list)
    assert len(entries) > 0

    # Find our inspector VM in the list.
    ours = [e for e in entries
            if (e.get("Name") or e.get("name")) == inspector_vm.name]
    assert len(ours) == 1, f"{inspector_vm.name} not in tart list"
    e = ours[0]

    # Fields the Go side relies on. Cased exactly as tart 2.x emits.
    assert "Name" in e, f"keys: {list(e.keys())}"
    assert "State" in e, f"keys: {list(e.keys())}"
    assert "Source" in e, f"keys: {list(e.keys())}"

    # State values we expect to see at any point.
    assert e["State"] in ("running", "stopped"), \
        f"unexpected state value: {e['State']!r}"
