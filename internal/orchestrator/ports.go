// Package orchestrator coordinates the host-side devm lifecycle:
// devm shell bootstrap, devm stop, devm teardown. Each command
// composes lower-level sandbox/lock primitives with sbx CLI calls
// to drive the user-facing flow.
package orchestrator

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/mtwaage/devm/internal/sandbox"
	"github.com/mtwaage/devm/internal/schema"
)

// portMapping mirrors the JSON shape of `sbx ports --json`.
type portMapping struct {
	HostIP      string `json:"host_ip"`
	HostPort    int    `json:"host_port"`
	SandboxPort int    `json:"sandbox_port"`
	Protocol    string `json:"protocol"`
}

// ReconcilePorts diffs cfg's desired port mappings against the
// sandbox's current published ports (queried via sbx ports --json)
// and applies the difference via sbx ports --publish / --unpublish.
// The sandbox must be running.
//
// Each service whose Canonical port is non-zero contributes one
// desired mapping: hostPort = cfg.Project.PortOffset + svc.Canonical,
// sandboxPort = svc.Canonical, protocol = tcp.
//
// All desired mappings bind 127.0.0.1; sbx normalizes its --json
// output to the same. Manual sbx ports --publish to other host IPs
// (e.g. 0.0.0.0) is not part of the devm.yaml model — such mappings
// will appear as "removes" on every reconcile.
func ReconcilePorts(sb *sandbox.Sandbox, cfg schema.Config) error {
	return ReconcilePortsWithRunner(sb, cfg, sandbox.DefaultRunner{})
}

// ReconcilePortsWithRunner is the testable inner. Callers pass an
// explicit Runner (production: DefaultRunner; tests: a stub).
func ReconcilePortsWithRunner(sb *sandbox.Sandbox, cfg schema.Config, r sandbox.Runner) error {
	desired := desiredMappings(cfg)
	current, err := currentMappings(sb, r)
	if err != nil {
		return err
	}

	adds, removes := diff(desired, current)

	for _, m := range adds {
		spec := fmt.Sprintf("%d:%d", m.HostPort, m.SandboxPort)
		if _, err := r.Output("sbx", "ports", sb.Name, "--publish", spec); err != nil {
			return fmt.Errorf("reconcile: publish %s: %w", spec, err)
		}
	}
	for _, m := range removes {
		spec := fmt.Sprintf("%d:%d", m.HostPort, m.SandboxPort)
		if _, err := r.Output("sbx", "ports", sb.Name, "--unpublish", spec); err != nil {
			return fmt.Errorf("reconcile: unpublish %s: %w", spec, err)
		}
	}
	return nil
}

// desiredMappings builds the desired set from the config. Services
// without a canonical port are skipped. Result is sorted by sandbox
// port for deterministic apply order.
func desiredMappings(cfg schema.Config) []portMapping {
	var out []portMapping
	for _, svc := range cfg.Services {
		if svc.Canonical == 0 {
			continue
		}
		out = append(out, portMapping{
			HostIP:      "127.0.0.1",
			HostPort:    cfg.Project.PortOffset + svc.Canonical,
			SandboxPort: svc.Canonical,
			Protocol:    "tcp",
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SandboxPort < out[j].SandboxPort })
	return out
}

// currentMappings reads `sbx ports <name> --json` and parses it.
func currentMappings(sb *sandbox.Sandbox, r sandbox.Runner) ([]portMapping, error) {
	out, err := r.Output("sbx", "ports", sb.Name, "--json")
	if err != nil {
		return nil, fmt.Errorf("reconcile: list ports: %w", err)
	}
	var maps []portMapping
	if len(out) == 0 {
		return maps, nil
	}
	if err := json.Unmarshal(out, &maps); err != nil {
		return nil, fmt.Errorf("reconcile: parse ports JSON: %w", err)
	}
	return maps, nil
}

// diff returns mappings to add and to remove. Two mappings match when
// HostPort + SandboxPort + Protocol match (HostIP treated as stable
// 127.0.0.1; sbx normalizes that anyway).
func diff(desired, current []portMapping) (adds, removes []portMapping) {
	key := func(m portMapping) string {
		return fmt.Sprintf("%d:%d/%s", m.HostPort, m.SandboxPort, m.Protocol)
	}
	desiredSet := map[string]portMapping{}
	for _, m := range desired {
		desiredSet[key(m)] = m
	}
	currentSet := map[string]portMapping{}
	for _, m := range current {
		currentSet[key(m)] = m
	}
	for k, m := range desiredSet {
		if _, ok := currentSet[k]; !ok {
			adds = append(adds, m)
		}
	}
	for k, m := range currentSet {
		if _, ok := desiredSet[k]; !ok {
			removes = append(removes, m)
		}
	}
	sort.Slice(adds, func(i, j int) bool { return adds[i].SandboxPort < adds[j].SandboxPort })
	sort.Slice(removes, func(i, j int) bool { return removes[i].SandboxPort < removes[j].SandboxPort })
	return adds, removes
}
