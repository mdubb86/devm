package orchestrator

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/serviceapi"
	"gopkg.in/yaml.v3"
)

// RunStatus collects read-only state for `devm status`.
func RunStatus(cfg schema.Config, tr *tart.Tart, repoRoot string) (StatusResult, error) {
	vmName := cfg.Project.VMName
	res := StatusResult{Sandbox: vmName}

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

	// Proxy health: TCP dial to localhost:443 within 500ms. If
	// launchd handed off the socket and the daemon is up, this
	// succeeds.
	conn, err := net.DialTimeout("tcp", "127.0.0.1:443", 500*time.Millisecond)
	if err == nil {
		res.ProxyHealthy = true
		_ = conn.Close()
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
	statusChanges, err := ComputeAllChanges(snapCfg, cfg, repoRoot)
	if err != nil {
		return res, fmt.Errorf("compute changes: %w", err)
	}
	for _, c := range statusChanges {
		if c.Bucket() == BucketLive {
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
