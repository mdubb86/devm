package main

import (
	"context"
	"fmt"
	"os"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/serviceapi"
)

// daemonHandshake does the daemon-sync fingerprint check (same as
// requireDaemonInSync — kept in cmd/devm because it needs the package
// Fingerprint var + resolvedSelfPath()) and, when the daemon reports this
// project's iron-proxy unhealthy, warns on stderr — reporting only, never
// mutating. `devm reconcile` is the sole heal path.
//
// ident is the daemon identity (prod vs. e2e); named "ident" rather
// than "cfg" here because cfg is the caller's project schema.Config —
// this function's own parameter is also named cfg, shadowing the
// package-level identity cfg, so callers must pass their own captured
// ident explicitly.
func daemonHandshake(ctx context.Context, ident identity.Config, cfg schema.Config) error {
	client := serviceapi.NewClient(ident)
	hs, err := client.Handshake(ctx, cfg.Project.Name)
	if err != nil {
		return nil // daemon down/unreachable — tolerated
	}
	if hs.Build.Fingerprint != "" && Fingerprint != "" && hs.Build.Fingerprint != Fingerprint {
		return fmt.Errorf(
			"devm daemon is out of sync with this CLI — API compatibility not guaranteed.\n"+
				"  daemon: %s (fingerprint %s)\n"+
				"  CLI:    %s (fingerprint %s)\n"+
				"Recovery: `devm install`",
			hs.Build.BinaryPath, hs.Build.Fingerprint, resolvedSelfPath(), Fingerprint,
		)
	}
	if hs.Proxy != nil && hs.Proxy.Status != serviceapi.ProxyOK && vmIsRunning(ctx, client, cfg.Project.Name) {
		// Skipped when the VM is stopped: `devm shell` / `devm start`
		// cold-start via /vm/start, which respawns iron-proxy fresh, so
		// the drift is transient and not actionable. If we can't tell
		// (endpoint missing, error), default to warning so real drift on
		// warm-attach paths still surfaces.
		fmt.Fprintf(os.Stderr, "warning: iron-proxy for %s is %s — run 'devm reconcile' to restore\n", cfg.Project.Name, hs.Proxy.Status)
	}
	return nil
}

// vmIsRunning asks the daemon whether the project's VM is currently up.
// Returns true on any error so callers err on the side of surfacing drift
// warnings when the state is unknowable.
func vmIsRunning(ctx context.Context, client *serviceapi.Client, projectName string) bool {
	resp, err := client.VMStatus(ctx, projectName)
	if err != nil {
		return true
	}
	return resp.Running
}
