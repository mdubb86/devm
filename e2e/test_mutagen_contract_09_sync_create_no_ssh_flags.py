"""Pin: `mutagen sync create` has NO ssh-configuration flags.

  -o key=value        → 'unknown shorthand flag'   (i.e. mutagen doesn't
                                                    forward to ssh)
  -i <path>           → --ignore, NOT ssh identity  (a real trap: looks
                                                    plausible but sends
                                                    the identity path
                                                    as an ignore pattern)

devm shells out ssh configuration through the system ssh_config
(internal/orchestrator/sshconfig_emit.go writes per-project Host blocks;
internal/serviceapi/mutagen.go points HOME at a devm-controlled dir so
the mutagen daemon's ssh child reads that config). This pin locks the
'we CANNOT put ssh knobs on the sync-create CLI' constraint so a future
'clever' refactor can't quietly reintroduce them and break silently.

Test 09 is a direct regression pin for the '-i /key -o Strict...'
experiment that shipped for a couple of iterations before we discovered
mutagen was interpreting -i as --ignore. The BUG that shipped was NOT a
build error — mutagen accepted -i, interpreted it as --ignore path, and
then the sync failed at a completely different layer.
"""
from __future__ import annotations

import pytest

from helpers import mutagen_contract as mc

pytestmark = pytest.mark.contract


def test_sync_create_rejects_dash_o_shorthand():
    """`-o <val>` is not a mutagen sync-create flag. If mutagen ever
    starts accepting `-o` (e.g. as an alias for ssh options), the
    quietly-broken behavior it had for us before returns."""
    with mc.daemon() as data_dir:
        r = mc.run(
            [
                "sync", "create",
                "-o", "StrictHostKeyChecking=yes",
                "--name", "dash-o-test",
                "/tmp/mut-alpha-doesnt-matter",
                "/tmp/mut-beta-doesnt-matter",
            ],
            data_dir=data_dir,
            check=False,
        )
        assert r.returncode != 0, (
            f"expected non-zero exit for `-o key=value` — a real accepting "
            f"of -o would silently mean ssh options are back on the CLI. "
            f"stderr={r.stderr!r}"
        )
        assert "unknown shorthand flag" in r.stderr and "'o'" in r.stderr, (
            f"expected 'unknown shorthand flag: o' in stderr. Got: "
            f"{r.stderr!r}"
        )


def test_sync_create_dash_i_is_ignore_not_ssh_identity():
    """`-i` is --ignore. Passing a keyfile-looking path through -i sends
    THAT PATH as an ignore pattern — no error surfaces, but the sync
    doesn't get an ssh key either. This is the exact trap the earlier
    experiment hit."""
    # `sync create --help` doesn't touch the daemon — help output is
    # the source of truth. Use a short-lived data_dir to satisfy the
    # helper's MUTAGEN_DATA_DIRECTORY plumbing; it's never written to.
    with mc.short_data_dir() as data_dir:
        r = mc.run(["sync", "create", "--help"], data_dir=data_dir, check=False)
    assert "-i, --ignore" in r.stdout, (
        f"expected `sync create --help` to advertise `-i` as `--ignore`. "
        f"If it starts advertising something else (like `--identity`), "
        f"the earlier experiment's shape becomes viable and we need to "
        f"revisit the assumption that ssh knobs cannot go on the CLI. "
        f"stdout tail: {r.stdout[-500:]!r}"
    )
    # Belt: no --identity flag exists.
    assert "--identity" not in r.stdout, (
        f"mutagen sync create advertised a --identity flag — the whole "
        f"'ssh knobs cannot go on the CLI' story needs rewriting. "
        f"stdout: {r.stdout!r}"
    )
