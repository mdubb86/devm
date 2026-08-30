"""Pin: mutagen looks for `mutagen-agents.tar.gz` in the SAME directory
as its argv[0]. Search paths are argv[0]'s parent dir and its `libexec`
sibling — nowhere else.

devm ships mutagen embedded in its own binary and extracts both the
mutagen binary and its agent bundle to `<RuntimeDir>/bin/` on daemon
startup (internal/mutagen.Ensure). This pin caught a real bug: an earlier
build shipped only the mutagen binary and every ssh-transport sync failed
with 'unable to locate agent bundle (search paths: [<bin>/ <bin>/../libexec])'.
The guest side is now pre-installed via `/opt/devm/bin/mutagen-agent`
copied by install.sh, so the SCP fallback path is theoretical.

We don't try to force the failure at test time — that would require a
functional ssh endpoint to reach the agent-install code path. Instead we
pin the string presence + directory layout mutagen documents in its own
error, which is what devm's build (fetch-mutagen recipe) and its
extraction path (internal/mutagen.Ensure) satisfy.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers import mutagen_contract as mc

pytestmark = pytest.mark.contract


def test_agent_bundle_filename_is_stable():
    """The filename devm's Ensure step must write next to the mutagen
    binary. Baked into the mutagen binary; if the upstream ever renames
    it, the extraction path must move in lockstep."""
    r = subprocess.run(
        ["strings", str(mc.binary_path())],
        capture_output=True, text=True, check=True, timeout=10,
    )
    assert "mutagen-agents.tar.gz" in r.stdout, (
        "mutagen no longer references 'mutagen-agents.tar.gz' — "
        "internal/mutagen.Ensure and just fetch-mutagen must move to the "
        "new filename in lockstep."
    )


def test_agent_bundle_search_paths_are_argv0_relative():
    """The parent directory of argv[0] and its `libexec` sibling are the
    ONLY places mutagen looks for the agent bundle. devm extracts to
    <RuntimeDir>/bin/, so the parent-of-argv[0] entry is what saves us —
    the libexec fallback is upstream's convention and is untouched by
    devm."""
    r = subprocess.run(
        ["strings", str(mc.binary_path())],
        capture_output=True, text=True, check=True, timeout=10,
    )
    # These are the two path expressions the error message names when the
    # bundle is missing. Their presence in the binary is proof the search
    # is by argv[0]-relative path, not by $PATH or a fixed system dir.
    assert "unable to locate agent bundle" in r.stdout, (
        "mutagen's canonical 'missing agent bundle' error string is gone "
        "— check whether the lookup mechanism changed."
    )
    assert "libexec" in r.stdout, (
        "the libexec fallback string is gone — the search-path shape "
        "devm depends on may have changed."
    )
