"""ssh: feature — jump denies an alias not in the project's ssh: list.

Two fixture sshds, `alias_allowed` and `alias_denied`. Project A lists
only `alias_allowed`. From A's guest, `ssh alias_denied` must fail at
the jump with "administratively prohibited" — not time out, not fail
on target auth. No second VM needed.
"""
from __future__ import annotations

import pytest

from helpers import e2e_project, local_sshd_target

pytestmark = pytest.mark.devm


@pytest.mark.slow
@pytest.mark.timeout(1500)
def test_jump_denies_untrusted():
    with e2e_project("ssh-deny") as p, \
            local_sshd_target() as alias_allowed, \
            local_sshd_target() as alias_denied:
        p.set_devm_yaml({"ssh": [alias_allowed]})
        p.start()
        # Sanity: the allowed alias actually works — otherwise a failure
        # on alias_denied could be masking the jump being broken outright.
        ok = p.run_in_guest(["ssh", alias_allowed, "true"], check=False)
        assert ok.returncode == 0, (
            f"baseline allowed alias must work: "
            f"stdout={ok.stdout!r} stderr={ok.stderr!r}"
        )
        res = p.run_in_guest(
            ["ssh", "-o", "ConnectTimeout=5", alias_denied, "true"],
            check=False,
        )
        assert res.returncode != 0, "cross-alias SSH must be denied"
        stderr_l = res.stderr.lower()
        assert (
            "administratively prohibited" in stderr_l
            or "channel request failed" in stderr_l
        ), f"want jump-level reject in stderr; got {res.stderr!r}"
