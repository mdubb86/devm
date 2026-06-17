package orchestrator

import (
	"fmt"
	"sort"

	"github.com/mdubb86/devm/internal/sandbox"
	"github.com/mdubb86/devm/internal/schema"
)

// ReconcileNetwork applies cfg's desired allowed domains to the
// sandbox via sbx policy allow. Idempotent: re-applying an existing
// allow returns success from sbx. Called at cold-start once the
// sandbox is exec-ready.
//
// Network policies are runtime-only ("local" provenance) so they can
// be removed at runtime via `sbx policy rm`. The render path does NOT
// emit network.allowedDomains in the kit — kit-provenance policies
// can't be cleanly removed without a recreate.
func ReconcileNetwork(sb *sandbox.Sandbox, cfg schema.Config) error {
	return ReconcileNetworkWithRunner(sb, cfg, sandbox.DefaultRunner{})
}

// ReconcileNetworkWithRunner is the testable inner.
func ReconcileNetworkWithRunner(sb *sandbox.Sandbox, cfg schema.Config, r sandbox.Runner) error {
	for _, d := range desiredDomains(cfg) {
		if err := applyNetworkAllow(r, sb, d); err != nil {
			return err
		}
	}
	return nil
}

// applyNetworkAllow runs `sbx policy allow network <sandbox> <domain>`.
// Shared by ReconcileNetworkWithRunner (cold-start) and apply_live's
// KindNetworkAdd case (live reconcile).
func applyNetworkAllow(r sandbox.Runner, sb *sandbox.Sandbox, domain string) error {
	if err := r.Run("sbx", "policy", "allow", "network", sb.Name, domain); err != nil {
		return fmt.Errorf("sbx policy allow network %s %s: %w", sb.Name, domain, err)
	}
	return nil
}

// applyNetworkRm runs `sbx policy rm network <sandbox> --resource <domain>`.
// Shared by apply_live's KindNetworkRemove case.
func applyNetworkRm(r sandbox.Runner, sb *sandbox.Sandbox, domain string) error {
	if err := r.Run("sbx", "policy", "rm", "network", sb.Name, "--resource", domain); err != nil {
		return fmt.Errorf("sbx policy rm network %s %s: %w", sb.Name, domain, err)
	}
	return nil
}

// desiredDomains is the sorted list of cfg.Network.AllowedDomains.
// Sorting gives deterministic apply order so failures are reproducible.
func desiredDomains(cfg schema.Config) []string {
	out := make([]string, len(cfg.Network.AllowedDomains))
	copy(out, cfg.Network.AllowedDomains)
	sort.Strings(out)
	return out
}
