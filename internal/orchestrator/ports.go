// Package orchestrator coordinates the host-side devm lifecycle:
// devm shell bootstrap, devm stop, devm teardown. Each command
// composes lower-level sandbox/lock primitives with sbx CLI calls
// to drive the user-facing flow.
package orchestrator

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

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
//
// Publishes are issued once. If `sbx ports --publish` returns success
// we trust it — empirically `sbx ports --json` can return [] briefly
// after a successful publish (during the window when sbx says
// `status: running` but the daemon's port listing isn't yet
// updated). Callers that need to wait for visibility should poll the
// list themselves.
func ReconcilePortsWithRunner(sb *sandbox.Sandbox, cfg schema.Config, r sandbox.Runner) error {
	desired := desiredMappings(cfg)
	current, err := currentMappings(sb, r)
	if err != nil {
		return err
	}

	adds, removes := diff(desired, current)

	for _, m := range adds {
		if err := publishWithVerify(sb, m, r); err != nil {
			return fmt.Errorf("reconcile: publish %d:%d: %w", m.HostPort, m.SandboxPort, err)
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

// errAlreadyPublished matches sbx's error when a port mapping is
// re-published while it already exists. Determined empirically; if
// sbx's wording changes this needs updating (a re-publish that should
// be a no-op would then surface as a hard error).
const errAlreadyPublished = "already published"

// publishWithVerify publishes a port mapping and confirms the mapping
// is *durably* applied — not just that `sbx ports --json` briefly shows
// it. sbx has two failure modes we have to defend against:
//
//  1. Brief visibility lag: a successful publish doesn't appear in
//     --json for a few hundred ms. We handle this by re-issuing publish
//     in a loop until --json shows the mapping (`verifyMappingVisible`).
//
//  2. Phantom publish: right after the cold-start anchor session is
//     killed, the first `sbx ports --publish` returns "Published ..."
//     AND the mapping briefly appears in --json — but it never durably
//     applies; the listing returns to [] within a second or two. The
//     second publish, by contrast, applies durably. Empirically about
//     5/6 cold starts trigger this on first publish. Documented in
//     docs/sbx-port-investigation.md.
//
// To survive (2), we don't trust a single verify-true. After
// `verifyMappingVisible` returns true, we hold and re-check. If the
// mapping is still there after the hold, it's real. If it disappeared,
// it was a phantom and we loop to re-publish.
const verifyHoldDuration = 500 * time.Millisecond

func publishWithVerify(sb *sandbox.Sandbox, m portMapping, r sandbox.Runner) error {
	spec := fmt.Sprintf("%d:%d", m.HostPort, m.SandboxPort)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := r.Output("sbx", "ports", sb.Name, "--publish", spec); err != nil {
			if !strings.Contains(err.Error(), errAlreadyPublished) {
				return fmt.Errorf("sbx ports --publish %s: %w", spec, err)
			}
		}
		if !verifyMappingVisible(sb, m, r, 3*time.Second) {
			continue
		}
		// Phantom defense: hold briefly and confirm the mapping is still
		// there. If it disappeared, we saw a phantom — loop and re-publish.
		time.Sleep(verifyHoldDuration)
		if verifyMappingVisible(sb, m, r, 250*time.Millisecond) {
			return nil
		}
	}
	return fmt.Errorf("published %s but never durably visible in sbx ports --json within 30s", spec)
}

// verifyMappingVisible polls sbx ports --json for up to `timeout`
// looking for the given mapping. Returns true if found.
func verifyMappingVisible(sb *sandbox.Sandbox, want portMapping, r sandbox.Runner, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		current, err := currentMappings(sb, r)
		if err == nil {
			for _, m := range current {
				if m.HostPort == want.HostPort && m.SandboxPort == want.SandboxPort {
					return true
				}
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

// waitForExecReady polls `sbx exec <name> true` until it succeeds,
// up to `timeout`. After sbx daemon reports `status: running`,
// there's a brief window during which exec calls and port publishes
// can be accepted but not yet effective. This check gates the
// orchestration on the sandbox being responsive to exec — a stronger
// readiness signal than `sbx ls` status alone.
func waitForExecReady(sb *sandbox.Sandbox, r sandbox.Runner, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if _, err := r.Output("sbx", "exec", sb.Name, "true"); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("sandbox %s not exec-ready within %s: %w", sb.Name, timeout, lastErr)
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
