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
	ApplyPackages(ctx context.Context, projectID string, snapCfg schema.Config, adds, removes []string) error
}

// realPackagesApplier is the production impl, wired by runner.go.
type realPackagesApplier struct {
	cfg     identity.Config
	tr      *tart.Tart
	sup     *supervisor.Supervisor
	denials *Denials

	// execScript is the test-injection seam for running the apt
	// converge script inside the guest. nil defaults to the
	// tart-backed implementation (tartExecScript). Same seam style as
	// spawnIronProxyFn.
	execScript func(ctx context.Context, vmName, script string) (int, string)
	// healthWait is the test-injection seam for post-respawn health
	// verification. nil defaults to waitIronProxyHealthy.
	healthWait func(addr string) bool
}

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
// apt mirrors, execs the converge script, then unconditionally
// restores the original allowlist before returning.
//
// snapCfg is the last-applied snapshot config — the state the running
// iron-proxy currently reflects — used both to rebuild the exact
// pre-change spawn config (ports, secrets, allowlist) and to decide
// whether Docker's apt repo host belongs in the transient window.
//
// The restore runs even when apt fails, so a failed install never
// leaves the egress window open — the caller sees an error either way,
// but the VM converges back to its steady-state policy regardless of
// which step failed.
func (a *realPackagesApplier) ApplyPackages(ctx context.Context, projectID string, snapCfg schema.Config, adds, removes []string) error {
	script := render.AptConvergeScript(adds, removes)
	if script == "" {
		return nil
	}

	orig, err := rebuildIronProxyConfig(a.cfg, projectID, snapCfg)
	if err != nil {
		return fmt.Errorf("apply packages: rebuild iron-proxy config: %w", err)
	}

	extra := aptEgressHosts(snapCfg.Docker)
	widened := orig
	widened.AllowList = append(append([]string{}, orig.AllowList...), extra...)

	if err := a.respawn(ctx, projectID, widened); err != nil {
		return fmt.Errorf("apply packages: widen egress: %w", err)
	}
	log.Printf("packages: transient apt-egress window open for %s (+%v)", projectID, extra)

	exitCode, stderr := a.exec()(ctx, projectID, script)

	// Restore UNCONDITIONALLY, before judging apt's result. A restore
	// failure fails the whole apply even when apt succeeded — the
	// window must not silently stay open (next apply-iron-proxy or
	// daemon restart converges it regardless).
	if rerr := a.respawn(ctx, projectID, orig); rerr != nil {
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
func (a *realPackagesApplier) respawn(ctx context.Context, projectID string, proxyCfg IronProxyConfig) error {
	key := supervisor.Key{ProjectID: projectID, Role: supervisor.RoleProxy}
	if st := a.sup.Status(key); st.Present && st.Running {
		if err := a.sup.Stop(ctx, key); err != nil && !errors.Is(err, supervisor.ErrNotFound) {
			return fmt.Errorf("stop iron-proxy: %w", err)
		}
	}
	if err := spawnIronProxyFn(ctx, a.cfg, a.sup, projectID, proxyCfg, a.denials); err != nil {
		return fmt.Errorf("spawn iron-proxy: %w", err)
	}
	if !a.wait()(proxyCfg.HTTPSListen) {
		return fmt.Errorf("iron-proxy spawned but did not bind %s within 2s", proxyCfg.HTTPSListen)
	}
	return nil
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
