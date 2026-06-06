"""sbx-quirk 06: characterize the publish-then-vanish phantom.

Observed during devm test_07 debugging on 2026-06-02: a `sbx ports
NAME --publish HOST:SBX` call issued immediately after exec-ready
returns rc=0, the mapping briefly appears in `sbx ports --json`, then
**disappears** ~1-2s later, and then **never reappears** even after
30+s of further polling. The mapping has to be re-published to come
back.

This is distinct from the originally documented Quirk #3 (the
"phantom publish" tied to anchor-session kill), because here the
anchor stays alive. The trigger appears to be timing of publish
relative to sandbox endpoint allocation — publishing too soon after
exec-ready races with the endpoint assignment, and the resulting
mapping is bound to a stale endpoint that gets garbage-collected.

This test runs a single publish + extensive polling, records the
visibility timeline, and asserts:

  1. The publish call itself returns rc=0 (or the documented
     transient "no container endpoint" error, which we ignore).
  2. **Eventually** (within `STABLE_DEADLINE`) the mapping is
     present for `STABLE_WINDOW` consecutive seconds without
     disappearing. If that never happens, the test FAILS with a
     timeline dump — and we know the orchestrator can't trust a
     single publish; it has to actively keep re-publishing.

The recorded timeline is always printed (even on pass) so a future
sbx-version regression that changes the phantom behavior is visible.
"""
from __future__ import annotations
import subprocess
import time

import pytest

from helpers import sbx
from helpers.sbx_kit import bring_up_anchored, materialize_kit

pytestmark = pytest.mark.sbx


HOST_PORT = 50260
SANDBOX_PORT = 8080
POLL_INTERVAL = 0.1    # 100ms
OBSERVATION = 15.0      # how long to poll after each publish
STABLE_WINDOW = 5.0     # mapping must be visible this many consecutive seconds
ITERATIONS = 3          # the phantom is racy; multiple cycles improve detection


def _has(name: str, host: int, sandbox_port: int) -> bool:
    for m in sbx.ports(name):
        if m.get("host_port") == host and m.get("sandbox_port") == sandbox_port:
            return True
    return False


def _publish_once_with_endpoint_retry(name: str, spec: str) -> int:
    """Publish, tolerating only the documented 'no container endpoint'
    transient. Returns the number of attempts before rc=0."""
    deadline = time.monotonic() + 10
    count = 0
    while time.monotonic() < deadline:
        r = subprocess.run(
            ["sbx", "ports", name, "--publish", spec],
            capture_output=True, timeout=15,
        )
        count += 1
        if r.returncode == 0:
            return count
        if b"no container endpoint" in r.stderr:
            time.sleep(0.5)
            continue
        pytest.fail(
            f"unexpected publish error rc={r.returncode}: "
            f"stdout={r.stdout!r} stderr={r.stderr!r}"
        )
    pytest.fail("publish never returned rc=0 within 10s")


def _observe(name: str, host: int, sandbox_port: int, duration: float):
    """Poll for `duration` seconds. Return (timeline, longest_visible_span)."""
    t0 = time.monotonic()
    timeline = []
    last_present = None
    while time.monotonic() - t0 < duration:
        present = _has(name, host, sandbox_port)
        now = time.monotonic() - t0
        if present != last_present:
            timeline.append((now, present))
            last_present = present
        time.sleep(POLL_INTERVAL)
    spans = []
    for i, (when, present) in enumerate(timeline):
        if present:
            end = timeline[i + 1][0] if i + 1 < len(timeline) else duration
            spans.append((when, end))
    longest = max((e - s for s, e in spans), default=0.0)
    return timeline, longest


@pytest.mark.timeout(180)
def test_first_publish_stays_and_republish_says_already_published(sandbox_name):
    """Pin pure-sbx publish behavior: first publish sticks, subsequent
    republishes return "already published".

    Sequence:
      1. bring up sandbox (anchor alive)
      2. publish HOST:SBX immediately after exec-ready
      3. wait 2 seconds — mapping should STILL be visible
      4. republish — should return "already published" (NOT "no
         container endpoint" as devm's flow does in test_07)

    Originally written to try to reproduce devm test_07's failure;
    instead pinned the OPPOSITE behavior: in pure sbx, mapping is
    stable, sbx remembers it, republishes are correctly idempotent.
    takes a long time (or never) to allocate a new one. This is
    the exact failure devm sees in test_07.
    """
    kit = materialize_kit()
    anchor = bring_up_anchored(sandbox_name, kit)
    spec = f"{HOST_PORT}:{SANDBOX_PORT}"
    timeline = []  # (elapsed, event, detail)
    t0 = time.monotonic()
    def log(event, detail=""):
        timeline.append((time.monotonic() - t0, event, detail))

    try:
        # Step 2: first publish
        r = subprocess.run(
            ["sbx", "ports", sandbox_name, "--publish", spec],
            capture_output=True, timeout=15,
        )
        log("publish_1", f"rc={r.returncode}")
        if r.returncode != 0:
            pytest.skip(
                f"first publish returned rc={r.returncode}; can't "
                f"reproduce the phantom-then-broken sequence. "
                f"stderr={r.stderr!r}"
            )

        # Was the mapping visible right after publish?
        visible_now = _has(sandbox_name, HOST_PORT, SANDBOX_PORT)
        log("visible_after_publish", str(visible_now))

        # Wait 2 seconds — mapping should STILL be visible in pure-sbx.
        time.sleep(2.0)
        visible_after_wait = _has(sandbox_name, HOST_PORT, SANDBOX_PORT)
        log("visible_after_2s", str(visible_after_wait))

        # Republish a few times — pure-sbx should return "already
        # published" every time (NOT "no container endpoint").
        already_count = 0
        endpoint_gone_count = 0
        republish_attempts = 0
        republish_deadline = time.monotonic() + 10
        while time.monotonic() < republish_deadline and republish_attempts < 5:
            republish_attempts += 1
            r = subprocess.run(
                ["sbx", "ports", sandbox_name, "--publish", spec],
                capture_output=True, timeout=15,
            )
            if r.returncode == 0:
                log(f"republish_{republish_attempts}", "rc=0 (re-add)")
            elif b"already published" in r.stderr:
                already_count += 1
                log(f"republish_{republish_attempts}", "already-published")
            elif b"no container endpoint" in r.stderr:
                endpoint_gone_count += 1
                log(f"republish_{republish_attempts}", "ENDPOINT-NOT-READY")
            else:
                log(f"republish_{republish_attempts}", f"other-err {r.stderr[:80]!r}")
            time.sleep(1.0)

        print(f"\n=== pure-sbx publish stability timeline ===")
        for when, event, detail in timeline:
            print(f"  +{when:6.2f}s  {event}: {detail}")
        print(f"  already-published: {already_count}/{republish_attempts}")
        print(f"  endpoint-not-ready: {endpoint_gone_count}/{republish_attempts}")
        print(f"=== END ===\n", flush=True)

        # Pure-sbx pins:
        # 1. Mapping is visible immediately after publish AND still
        #    visible after 2s (no phantom in pure-sbx).
        # 2. Republishes return "already published" consistently
        #    (sbx remembers the mapping; endpoint stays alive).
        # If endpoint-not-ready appears here, the sbx daemon may have
        # acquired the same bug devm hits — would be a big shift.
        assert visible_now, "mapping not visible immediately after publish"
        assert visible_after_wait, "mapping vanished within 2s (phantom in pure-sbx)"
        assert already_count >= 3, (
            f"expected sbx to return 'already published' on republishes, "
            f"got {already_count}/{republish_attempts} already-published "
            f"and {endpoint_gone_count}/{republish_attempts} endpoint-not-ready"
        )
        assert endpoint_gone_count == 0, (
            f"sbx returned 'no container endpoint' on a republish — "
            f"the bug devm hits in test_07 is now reproducing in pure "
            f"sbx ({endpoint_gone_count}/{republish_attempts} times). "
            f"sbx behavior may have shifted; revisit Quirk #6."
        )
    finally:
        subprocess.run(
            ["sbx", "ports", sandbox_name, "--unpublish", spec],
            capture_output=True, timeout=10,
        )
        if anchor.poll() is None:
            anchor.kill()
            try:
                anchor.wait(timeout=3)
            except Exception:
                pass
        sbx.sandbox_rm(sandbox_name)
        kit.cleanup()


@pytest.mark.timeout(300)
def test_publish_cycle_stable_across_iterations(sandbox_name):
    """Across `ITERATIONS` publish-observe cycles, every cycle's
    mapping reaches a visible span of at least `STABLE_WINDOW`
    seconds within `OBSERVATION` seconds of the publish.

    Between iterations we unpublish + republish — pins that
    pure-sbx is stable across multiple publish cycles, not just on
    the first publish."""
    kit = materialize_kit()
    anchor = bring_up_anchored(sandbox_name, kit)
    spec = f"{HOST_PORT}:{SANDBOX_PORT}"
    results = []  # (iter_index, publish_count, timeline, longest)
    try:
        for it in range(ITERATIONS):
            if it > 0:
                # Unpublish to reset state; ignore errors (mapping may
                # have vanished on its own already).
                subprocess.run(
                    ["sbx", "ports", sandbox_name, "--unpublish", spec],
                    capture_output=True, timeout=10,
                )
                time.sleep(1)  # let sbx settle between cycles
            publish_count = _publish_once_with_endpoint_retry(sandbox_name, spec)
            timeline, longest = _observe(
                sandbox_name, HOST_PORT, SANDBOX_PORT, OBSERVATION,
            )
            results.append((it, publish_count, timeline, longest))

        # Always print all timelines so a regression is debuggable.
        print(f"\n=== publish-phantom timelines (sandbox={sandbox_name}) ===")
        for it, publish_count, timeline, longest in results:
            print(f"  --- iter {it} (publish_count_to_rc0={publish_count}, "
                  f"longest_visible_span={longest:.2f}s) ---")
            for when, present in timeline:
                tag = "VISIBLE" if present else "GONE"
                print(f"    +{when:6.2f}s  {tag}")
            if not timeline:
                print(f"    (no state changes)")
        print(f"=== END ===\n", flush=True)

        # Assertion: every iteration must reach STABLE_WINDOW.
        failures = [
            (it, longest) for it, _, _, longest in results
            if longest < STABLE_WINDOW
        ]
        assert not failures, (
            f"{len(failures)}/{len(results)} iterations failed to reach "
            f"{STABLE_WINDOW}s of continuous visibility within "
            f"{OBSERVATION}s. Failing iters: {failures}. The phantom-"
            f"then-gone-forever behavior reproduces; the orchestrator "
            f"must actively re-publish until stability."
        )
    finally:
        subprocess.run(
            ["sbx", "ports", sandbox_name, "--unpublish", spec],
            capture_output=True, timeout=10,
        )
        if anchor.poll() is None:
            anchor.kill()
            try:
                anchor.wait(timeout=3)
            except Exception:
                pass
        sbx.sandbox_rm(sandbox_name)
        kit.cleanup()
