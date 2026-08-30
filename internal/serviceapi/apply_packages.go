package serviceapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/mdubb86/devm/internal/daemonlog"
	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/render"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/supervisor"
)

// PackagesApplier is the daemon-internal contract for converging apt
// packages on a running VM. Tests fake it in the reconcile handler.
type PackagesApplier interface {
	ApplyPackages(ctx context.Context, projectID string, snapCfg schema.Config, macCwd string, adds, removes []string) error
}

// realPackagesApplier is the production impl, wired by runner.go.
type realPackagesApplier struct {
	cfg identity.Config
	tr  *tart.Tart
	sup *supervisor.Supervisor

	// execScript is the test-injection seam for running the apt
	// converge script inside the guest. nil defaults to the
	// tart-backed implementation (tartExecScript). Same seam style as
	// spawnIronProxyFn.
	execScript func(ctx context.Context, vmName, script string) (int, string)
	// healthWait is the test-injection seam for post-respawn health
	// verification. nil defaults to waitIronProxyHealthy.
	healthWait func(addr string) bool
}

var _ PackagesApplier = (*realPackagesApplier)(nil)

// aptEgressHosts returns the egress hosts a transient apt-converge
// window must allow beyond the project's steady-state allowlist:
// Debian's package mirrors, plus Docker's apt repo host when the
// project has Docker enabled (get.docker.com's install script adds a
// repo on download.docker.com).
func aptEgressHosts(dockerEnabled bool) []string {
	hosts := []string{"deb.debian.org", "security.debian.org"}
	if dockerEnabled {
		hosts = append(hosts, "download.docker.com")
	}
	return hosts
}

// ApplyPackages converges the guest's apt package set: it widens the
// project's iron-proxy egress allowlist just long enough to reach the
// apt mirrors, execs the converge script, then restores the original
// allowlist before returning.
//
// snapCfg is the last-applied snapshot config — the state the running
// iron-proxy currently reflects — used both to rebuild the exact
// pre-change spawn config (ports, secrets, allowlist) and to decide
// whether Docker's apt repo host belongs in the transient window.
//
// Restore contract: once the widen respawn has actually invoked
// spawnIronProxyFn (config written, process started — regardless of
// whether that respawn call itself then failed on health-wait), a
// restore to the original allowlist is ALWAYS attempted before
// ApplyPackages returns — either the ordinary unconditional restore
// after a successful widen+exec, or a best-effort restore (failure
// logged, not returned) when the widen respawn itself failed after
// spawning. The only case with no restore attempt at all is a widen
// respawn that failed BEFORE spawnIronProxyFn was ever invoked (e.g.
// stopping the still-running old process failed) — nothing changed, so
// there is nothing to undo. A failure of the unconditional (post-exec)
// restore IS surfaced as this call's error even when apt itself
// succeeded; a failure of the best-effort (post-widen-failure) restore
// is not — the widen error is what's returned in that case.
//
// Caller contract: ApplyPackages does not acquire the project lock
// itself. The caller (the /vm/reconcile handler, via
// locks.Lock(projectID)) must hold it for the entire call — concurrent
// apply-iron-proxy or watchdog activity during the transient window
// would race this call's stop/spawn pairs against its own.
func (a *realPackagesApplier) ApplyPackages(ctx context.Context, projectID string, snapCfg schema.Config, macCwd string, adds, removes []string) error {
	script := render.AptConvergeScript(adds, removes)
	if script == "" {
		return nil
	}

	orig, err := rebuildIronProxyConfig(a.cfg, projectID, snapCfg, macCwd)
	if err != nil {
		return fmt.Errorf("apply packages: rebuild iron-proxy config: %w", err)
	}

	// Widen by the apt hosts not already covered by the steady-state
	// allowlist — mirrors docker.EffectiveAllowlist's own
	// already-present dedup so a project that (for whatever reason)
	// already allows deb.debian.org doesn't get a duplicated entry.
	extraHosts := aptEgressHosts(snapCfg.Docker)
	existing := make(map[string]struct{}, len(orig.AllowList))
	for _, h := range orig.AllowList {
		existing[h] = struct{}{}
	}
	added := make([]string, 0, len(extraHosts))
	for _, h := range extraHosts {
		if _, ok := existing[h]; ok {
			continue
		}
		added = append(added, h)
	}
	widened := orig
	widened.AllowList = append(append([]string{}, orig.AllowList...), added...)

	spawned, err := a.respawn(ctx, projectID, widened)
	if err != nil {
		if spawned {
			// spawnIronProxyFn already succeeded before this respawn
			// call failed (a health-wait timeout) — the widened config
			// may already be live on disk and running, and the
			// supervisor's own crash-restart would otherwise keep
			// respawning it widened. Try once, best-effort, to restore
			// the original egress; a failure here is logged but does
			// NOT change the error surfaced to the caller — the widen
			// failure is still the actionable one. Runs detached from
			// ctx (bounded by the health-wait budget) so a cancelled
			// request can't cut this restore attempt short too.
			if _, rerr := a.respawn(context.WithoutCancel(ctx), projectID, orig); rerr != nil {
				daemonlog.Errorf("packages: best-effort restore after widen failure for %s: %v", projectID, rerr)
			}
		}
		return fmt.Errorf("apply packages: widen egress: %w", err)
	}
	log.Printf("packages: transient apt-egress window open for %s (+%v)", projectID, added)

	exitCode, stderr := a.exec()(ctx, projectID, script)

	// Restore UNCONDITIONALLY, before judging apt's result. Detached
	// from ctx (bounded by the health-wait budget) so a cancelled
	// request can't degrade the restore's own stop/spawn pair. A
	// restore failure here fails the whole apply even when apt
	// succeeded — the window must not silently stay open (a later
	// apply-iron-proxy or daemon restart converges it regardless, but
	// not immediately).
	if _, rerr := a.respawn(context.WithoutCancel(ctx), projectID, orig); rerr != nil {
		daemonlog.Errorf("packages: restore egress for %s: %v", projectID, rerr)
		return fmt.Errorf("apply packages: restore egress: %w", rerr)
	}
	log.Printf("packages: transient apt-egress window closed for %s", projectID)

	if exitCode != 0 {
		return fmt.Errorf("apply packages: apt exit %d: %s", exitCode, stderr)
	}
	return nil
}

// respawn stops the project's current iron-proxy (only when the
// supervisor reports it present and running) and spawns a fresh one
// from proxyCfg, then waits for it to bind before returning. Mirrors
// apply_iron_proxy.go's stop -> spawnIronProxyFn -> health-wait
// sequence and ironproxy_watchdog.go's respawnIronProxyFromState.
//
// spawned reports whether spawnIronProxyFn was actually invoked and
// itself returned no error — true even when the subsequent health-wait
// times out, since by then the given config is already written to disk
// and the process already started. Callers use this to decide whether a
// failed respawn needs a compensating restore attempt: spawned=false
// means nothing changed and there's nothing to undo (e.g. the stop of
// the prior process failed before any spawn was attempted).
func (a *realPackagesApplier) respawn(ctx context.Context, projectID string, proxyCfg IronProxyConfig) (spawned bool, err error) {
	key := supervisor.Key{ProjectID: projectID, Role: supervisor.RoleProxy}
	if st := a.sup.Status(key); st.Present && st.Running {
		if err := a.sup.Stop(ctx, key); err != nil && !errors.Is(err, supervisor.ErrNotFound) {
			return false, fmt.Errorf("stop iron-proxy: %w", err)
		}
	}
	if err := spawnIronProxyFn(ctx, a.cfg, a.sup, projectID, proxyCfg); err != nil {
		return false, fmt.Errorf("spawn iron-proxy: %w", err)
	}
	if !a.wait()(proxyCfg.HTTPSListen) {
		return true, fmt.Errorf("iron-proxy spawned but did not bind %s within 2s", proxyCfg.HTTPSListen)
	}
	return true, nil
}

// exec returns execScript, defaulting to the tart-backed implementation.
func (a *realPackagesApplier) exec() func(ctx context.Context, vmName, script string) (int, string) {
	if a.execScript != nil {
		return a.execScript
	}
	return a.tartExecScript
}

// wait returns healthWait, defaulting to waitIronProxyHealthy.
func (a *realPackagesApplier) wait() func(addr string) bool {
	if a.healthWait != nil {
		return a.healthWait
	}
	return waitIronProxyHealthy
}

// tartExecScript is the production execScript: pipes script over `tart
// exec -i` as a bash script, capturing exit code and stderr.
func (a *realPackagesApplier) tartExecScript(ctx context.Context, vmName, script string) (int, string) {
	r := a.tr.ExecStdin(ctx, vmName, strings.NewReader(script), []string{"bash", "-e", "-o", "pipefail"})
	return r.ExitCode, r.Stderr
}
