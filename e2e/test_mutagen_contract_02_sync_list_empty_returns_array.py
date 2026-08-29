"""Pin: `mutagen sync list --template '{{json .}}'` returns an empty
JSON array (`[]`) when no sessions exist, not `null` and not an error.

devm's mutagen.CLI.SyncList (internal/mutagen/cli.go) parses this
output with `json.Unmarshal`. `null` would unmarshal to a nil slice
(handled fine but semantically wrong); a non-zero exit code with
"no sessions" in stderr would surface as a fatal error to callers
(BuildEntities → SetupPhase → provisionAndAttach). Both alternatives
would break devm's expectation that an empty session list is a normal,
non-error state — the exact state a fresh mutagen daemon is in every
time devm boots one.
"""
from __future__ import annotations

import pytest

from helpers import mutagen_contract as mc

pytestmark = pytest.mark.contract


def test_sync_list_empty_returns_json_array():
    with mc.daemon() as data_dir:
        r = mc.run(
            ["sync", "list", "--template", "{{json .}}"],
            data_dir=data_dir,
        )
        # Non-zero exit would surface as an error in devm's SyncList
        # and break every code path that lists (BuildEntities,
        # SetupPhase's find-existing check, StopPhase's flush pass).
        assert r.returncode == 0, (
            f"empty-state `sync list` must exit 0, got rc={r.returncode!r} "
            f"stderr={r.stderr!r}"
        )

        stdout = r.stdout.strip()
        # Pin the EXACT wire shape — devm.CLI.SyncList feeds this
        # straight to json.Unmarshal([]SyncSession).
        assert stdout == "[]", (
            f"expected empty state to be literal `[]` (empty JSON array), "
            f"got {stdout!r}. `null` would unmarshal to a nil slice — "
            f"semantically fine but not what devm's tests pin — and would "
            f"quietly change behavior on the next mutagen release."
        )
