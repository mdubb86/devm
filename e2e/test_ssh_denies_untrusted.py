"""ssh: feature — jump denies an alias not in the project's ssh: list.

Starts two projects A and B. A's ssh: lists A's own vm alias only. B
is running (so its vm alias resolves in Mac config) but is not in A's
ssh: list. From A's guest, `ssh devm-<Bvm>` must fail with the jump
denying — not time out, not fail on target auth.
"""
from __future__ import annotations

import pytest

from helpers import e2e_project, ssh_add_and_cleanup

pytestmark = pytest.mark.devm


@pytest.mark.slow
@pytest.mark.timeout(1500)
def test_jump_denies_untrusted():
    with e2e_project("ssh-deny-a") as a, e2e_project("ssh-deny-b") as b:
        a.set_devm_yaml({"ssh": [f"devm-{a.vm_name}"]})
        a.start()
        # Just making devm-<b.vm_name> resolvable in Mac ssh config —
        # B is NOT in A's ssh: list, so A's jump must refuse to dial it.
        b.start()
        with ssh_add_and_cleanup(a.client_key_path):
            res = a.run_in_guest(
                ["ssh", "-o", "ConnectTimeout=5",
                 f"devm-{b.vm_name}", "true"],
                check=False,
            )
            assert res.returncode != 0, "cross-project SSH must be denied"
            # Jump's reject surfaces as "administratively prohibited" —
            # exact wording varies by OpenSSH client but the key strings
            # are stable.
            stderr_l = res.stderr.lower()
            assert (
                "administratively prohibited" in stderr_l
                or "channel request failed" in stderr_l
            ), f"want jump-level reject in stderr; got {res.stderr!r}"
