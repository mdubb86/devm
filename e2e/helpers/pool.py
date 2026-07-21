"""Pool-IP lookup — softnet-era replacement for `tart ip`.

Post-B3, the Mac reaches a project's services (direct or proxied) via
the project's allocated pool IP (127.42.0.N), not the VM's own IP on
vmnet. This reads that address out of the daemon's persisted state
snapshot rather than shelling out to `tart ip`.
"""
from pathlib import Path
import json
import os


def pool_ip(project_id: str) -> str:
    """Return the project's allocated pool IP (127.42.0.N) by reading
    the daemon's state snapshot. Post-B3, this is the single canonical
    address the Mac uses to reach ANY of the project's services —
    both direct-mode (via softnet ingress) and non-direct (via daemon
    proxy). Replaces pre-B3's `tart ip <vm>` for tests written before
    softnet took over the VM's network path from vmnet."""
    state_dir = Path(os.path.expanduser("~/Library/Application Support/devm-e2e/state"))
    path = state_dir / f"{project_id}.json"
    if not path.exists():
        raise RuntimeError(f"no daemon state snapshot at {path} — is the VM up?")
    return json.loads(path.read_text())["project_ip"]
