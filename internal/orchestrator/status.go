package orchestrator

import (
	"context"
	"fmt"
	"time"

	"github.com/mdubb86/devm/internal/reconcile"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/serviceapi"
	"gopkg.in/yaml.v3"
)

// RunStatus collects read-only state for `devm status`. cliFingerprint
// is the CLI's own compiled-in Fingerprint constant, threaded through
// so ProbeDaemon can report drift without orchestrator importing
// cmd/devm.
func RunStatus(cfg schema.Config, tr *tart.Tart, repoRoot, cliFingerprint string) (StatusResult, error) {
	vmName := cfg.Project.VMName
	res := StatusResult{
		HasProject: true,
		Sandbox:    vmName,
		Daemon:     ProbeDaemon(context.Background(), cliFingerprint),
	}

	// Routing status — query the daemon's /routes endpoint. Runs
	// unconditionally so users see it whenever they `devm status`. On
	// error we leave Routing zero-valued; the format layer renders that
	// as proxy-unreachable without breaking the rest of status.
	c := serviceapi.NewClient()
	if routing, err := c.RoutingStatusFromDaemon(context.Background()); err == nil {
		res.Routing = routing
	}

	dnsCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := serviceapi.CheckDNSHealth(dnsCtx); err == nil {
		res.DNSHealthy = true
	} else {
		res.DNSHealthy = false
		res.DNSError = err.Error()
	}

	// CA trust state — read-only, no sudo.
	trusted, _ := serviceapi.CheckCATrusted()
	res.CATrusted = trusted

	// Proxy health: ask the daemon over the unix socket. Previously
	// this was a TCP dial to 127.0.0.1:443 with immediate close — but
	// every call dropped mid-TLS-handshake and each one spammed a
	// "TLS handshake error … EOF" line into the daemon log, which
	// masked real errors. The unix-socket probe has zero on-wire
	// footprint for the reverse-proxy actor.
	proxyCtx, proxyCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer proxyCancel()
	if ready, err := c.ProxyReady(proxyCtx); err == nil {
		res.ProxyHealthy = ready
		if !ready {
			res.ProxyError = "reverse-proxy actor not started (launchd sockets not handed off)"
		}
	} else {
		res.ProxyError = err.Error()
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

	if state != "running" {
		return res, nil
	}

	// Sessions (best-effort via tart exec).
	res.Sessions = probeSessions(tr, vmName)

	// Pending changes vs snapshot.
	snapStr, err := ReadSnapshot(tr, vmName)
	if err != nil {
		return res, fmt.Errorf("read snapshot: %w", err)
	}
	var snapCfg schema.Config
	if snapStr == "" {
		snapCfg = cfg
	} else {
		if err := yaml.Unmarshal([]byte(snapStr), &snapCfg); err != nil {
			return res, fmt.Errorf("parse snapshot: %w", err)
		}
	}
	statusChanges, err := reconcile.ComputeAllChanges(snapCfg, cfg, repoRoot)
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
