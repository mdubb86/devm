package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/reconcile"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/serviceapi"
)

// RunStatus collects read-only state for `devm status`. ident is the
// daemon identity (prod vs. e2e) this status probe operates under;
// named "ident" rather than "cfg" here because cfg is already the
// project's schema.Config. cliFingerprint is the CLI's own compiled-in
// Fingerprint constant, threaded through so ProbeDaemon can report
// drift without orchestrator importing cmd/devm.
func RunStatus(ident identity.Config, cfg schema.Config, tr *tart.Tart, repoRoot, cliFingerprint string) (StatusResult, error) {
	vmName := cfg.Project.Name
	res := StatusResult{
		HasProject: true,
		Sandbox:    vmName,
		Daemon:     ProbeDaemon(context.Background(), ident, cliFingerprint),
	}

	// Routing status — query the daemon's /routes endpoint. Runs
	// unconditionally so users see it whenever they `devm status`. On
	// error we leave Routing zero-valued; the format layer renders that
	// as proxy-unreachable without breaking the rest of status.
	c := serviceapi.NewClient(ident)
	if routing, err := c.RoutingStatusFromDaemon(context.Background()); err == nil {
		res.Routing = routing
	}

	dnsCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := serviceapi.CheckDNSHealth(dnsCtx, ident); err == nil {
		res.DNSHealthy = true
	} else {
		res.DNSHealthy = false
		res.DNSError = err.Error()
	}

	// CA trust state — read-only, no sudo.
	trusted, _ := serviceapi.CheckCATrusted(ident)
	res.CATrusted = trusted

	// Approve-gate state — informational only, never blocks `devm
	// status`. A 404 means the daemon predates the approve gate; the
	// format layer silently omits the line for that case (backward
	// compat). Any other error is reported but doesn't fail status.
	approveCtx, approveCancel := context.WithTimeout(context.Background(), 2*time.Second)
	approveResp, approveErr := c.ApproveState(approveCtx, cfg.Project.Name)
	approveCancel()
	switch {
	case approveErr == nil:
		res.ApproveState = &approveResp
	case errors.Is(approveErr, serviceapi.ErrApproveStateUnsupported):
		// Old daemon — leave res.ApproveState and res.ApproveError unset.
	default:
		res.ApproveError = approveErr.Error()
	}

	// Proxy health: aggregate across every running project's reverse-
	// proxy listener pair, derived per-tick from the watchdog's cache
	// (ProjectRow.ProxyListenerHealth, see watchdog_check_proxy_listener.go)
	// via /status/all. Previously this asked a single boot-time flag
	// ("did launchd hand off :80/:443 at daemon startup") that stayed
	// true even after a listener died mid-run — it could never catch
	// the drift the watchdog now detects and repairs. No running
	// projects is vacuously healthy: nothing to be unhealthy about.
	proxyCtx, proxyCancel := context.WithTimeout(context.Background(), 2*time.Second)
	rows, rowsErr := c.StatusAll(proxyCtx)
	proxyCancel()
	if rowsErr == nil {
		allHealthy := true
		var unhealthy []string
		for _, row := range rows {
			// Stopped/absent projects have nothing to probe; orphaned
			// rows carry no cache-backed listener signal at all (see
			// ProjectStatus.Orphaned).
			if !row.VMRunning || row.Orphaned {
				continue
			}
			if !row.ProxyListenerHealth {
				allHealthy = false
				unhealthy = append(unhealthy, row.Name)
			}
		}
		res.ProxyHealthy = allHealthy
		if !allHealthy {
			sort.Strings(unhealthy)
			res.ProxyError = fmt.Sprintf("proxy listener unhealthy for: %s", strings.Join(unhealthy, ", "))
		}

		// Mutagen health, this project only — Task 9's mutagen-monitor
		// subscriber writes ProjectRow.MutagenHealth in near-real-time
		// (see mutagen_monitor.go), and /status/all is already fetched
		// above for the proxy aggregate, so reuse it rather than a
		// second round trip.
		for _, row := range rows {
			if row.Name == vmName {
				res.MutagenHealth = string(row.MutagenHealth)
				break
			}
		}
	} else {
		res.ProxyError = rowsErr.Error()
	}

	vms, err := tr.List(context.Background())
	if err != nil {
		// List failure: report absent; don't surface the error — the
		// format layer handles absent gracefully and the user may be
		// running status before tart is installed.
		res.State = "absent"
		return res, nil
	}
	state := "absent"
	for _, vm := range vms {
		if vm.Name == vmName {
			if vm.Running {
				state = "running"
			} else {
				state = "stopped"
			}
			break
		}
	}
	res.State = state

	// Per-project iron-proxy verdict, via /handshake — read-only report.
	// `devm reconcile` is the sole heal path; status never mutates. A
	// stopped (or absent) VM has no live proxy by design — reconcile's
	// KindIronProxyDown handler only fires for a running VM — so we
	// don't even ask, and res.ProxyHealth stays nil: the format layer
	// omits the iron-proxy line and the caller's exit-4 check
	// (res.ProxyHealth != nil && ...) never fires for a stopped project.
	if state == "running" {
		handshakeCtx, handshakeCancel := context.WithTimeout(context.Background(), 2*time.Second)
		hs, hsErr := c.Handshake(handshakeCtx, cfg.Project.Name)
		handshakeCancel()
		if hsErr == nil {
			res.ProxyHealth = hs.Proxy
		}
		// hsErr != nil: daemon unreachable — res.ProxyHealth stays nil
		// and the format layer omits the iron-proxy line rather than
		// claiming a status we don't have.

		egCtx, egCancel := context.WithTimeout(context.Background(), 2*time.Second)
		if eg, err := c.EgressStatus(egCtx, cfg.Project.Name); err == nil {
			res.Egress = eg
		}
		egCancel()
	}

	if state != "running" {
		return res, nil
	}

	// Sessions (best-effort via tart exec).
	res.Sessions = probeSessions(tr, vmName)

	// Pending changes vs the daemon-side state snapshot. A missing or
	// unreadable snapshot degrades to "current is the baseline" — same
	// semantics reconcile itself uses on first apply.
	snapCfg := cfg
	var lastAppliedTemplates map[string]string
	if stateSnap, sErr := serviceapi.ReadStateSnapshot(ident, cfg.Project.Name); sErr == nil && stateSnap != nil {
		snapCfg = stateSnap.Cfg
		lastAppliedTemplates = stateSnap.TemplateContents
	}
	// devm status observes without converging: empty
	// currentBundleFingerprint opts out of bundle-drift emission.
	statusChanges, err := reconcile.ComputeAllChanges(snapCfg, cfg, repoRoot, ident.RuntimeDir(), lastAppliedTemplates, nil, nil, "", "")
	if err != nil {
		return res, fmt.Errorf("compute changes: %w", err)
	}
	for _, c := range statusChanges {
		if c.Bucket() == reconcile.BucketLive {
			res.PendingLive++
		} else {
			res.PendingRecreate++
		}
	}
	return res, nil
}

// probeSessions returns active interactive pty sessions in the VM by
// running the probe script via tart exec. Returns nil on any error —
// callers treat sessions as best-effort.
func probeSessions(tr *tart.Tart, vmName string) []Session {
	r := tr.Exec(context.Background(), vmName, []string{"bash", "-c", probeScript})
	if r.ExitCode != 0 {
		return nil
	}
	return parseSessions(r.Stdout)
}
