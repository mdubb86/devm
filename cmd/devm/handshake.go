package main

import (
	"context"
	"fmt"
	"os"

	"github.com/mdubb86/devm/internal/orchestrator"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/serviceapi"
)

// daemonHandshake does the daemon-sync fingerprint check (same as
// requireDaemonInSync — kept in cmd/devm because it needs the package
// Fingerprint var + resolvedSelfPath()) and, when the daemon reports this
// project's iron-proxy unhealthy, heals it best-effort (logs on failure,
// never blocks the user's command).
func daemonHandshake(ctx context.Context, cfg schema.Config) error {
	hs, err := serviceapi.NewClient().Handshake(ctx, cfg.Project.ID)
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
	if hs.Proxy != nil && hs.Proxy.Status != serviceapi.ProxyOK {
		if herr := orchestrator.HealIronProxy(ctx, cfg); herr != nil {
			fmt.Fprintf(os.Stderr, "warning: iron-proxy heal for %s failed: %v\n", cfg.Project.ID, herr)
		}
	}
	return nil
}
