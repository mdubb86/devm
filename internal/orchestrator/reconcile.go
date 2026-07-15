package orchestrator

import (
	"context"
	"fmt"

	"github.com/mdubb86/devm/internal/docker"
	"github.com/mdubb86/devm/internal/reconcile"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/secret"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/mdubb86/devm/internal/serviceapi/sshkeys"
)

// ReconcileOptions controls RunReconcile's behaviour. Placeholder for
// future CLI-facing switches — approval / non-interactive / JSON
// decisions for a pending teardown now live in cmd/devm/reconcile.go:
// RunReconcile itself never prompts and never executes a recreate.
type ReconcileOptions struct{}

// RunReconcile POSTs cfg + secret_hashes to the daemon's /vm/reconcile
// endpoint. Live changes apply daemon-side and come back on Applied.
// Iron-proxy-restart changes come back on AppliedIronProxy and this
// function then dispatches /vm/apply-iron-proxy with the freshly-
// resolved allowlist + secrets. Teardown-required changes come back on
// RecreateRequired and the caller decides whether to prompt.
//
// RunReconcile does not prompt or execute a recreate itself: it
// returns the daemon's classification and lets cmd/devm/reconcile.go
// decide whether to prompt, and to run the teardown + start helpers
// `devm teardown` / `devm shell` already use, on approval.
//
// Return codes: 0 on success (regardless of whether a recreate is
// pending — the caller inspects res.RecreateRequired), -1 when the
// daemon call itself failed.
func RunReconcile(cfg schema.Config, tr *tart.Tart, repoRoot string, opts ReconcileOptions) (int, ReconcileResult, error) {
	client := serviceapi.NewClient()

	// Resolve secrets CLI-side for the hash map AND for the possible
	// downstream ApplyIronProxy call. resolveSecretBindings walks env
	// values for !secret refs; if none, bindings is nil.
	bindings, err := resolveSecretBindings(cfg, secret.NewMacKeychain())
	if err != nil {
		return -1, ReconcileResult{}, fmt.Errorf("resolve secrets: %w", err)
	}
	hashes := SecretHashesFromBindings(bindings)

	// Load SSH keys CLI-side and pass to the daemon.
	authPub, err := sshkeys.EnsureProjectKeypair(cfg.Project.Name)
	if err != nil {
		return -1, ReconcileResult{}, fmt.Errorf("ensure ssh keypair: %w", err)
	}
	hostPriv, hostPub, err := sshkeys.EnsureProjectHostKey(cfg.Project.Name)
	if err != nil {
		return -1, ReconcileResult{}, fmt.Errorf("ensure ssh host key: %w", err)
	}

	resp, err := client.Reconcile(context.Background(), serviceapi.VMReconcileRequest{
		Name:                cfg.Project.Name,
		Cfg:                 cfg,
		WorkspaceHostPath:   repoRoot,
		SecretHashes:        hashes,
		SSHAuthorizedPubkey: authPub,
		SSHHostPriv:         hostPriv,
		SSHHostPub:          hostPub,
	})
	if err != nil {
		return -1, ReconcileResult{}, fmt.Errorf("reconcile: %w", err)
	}

	// If the daemon reports iron-proxy-restart changes, dispatch
	// ApplyIronProxy. Auto-apply — no prompt (matches live-path UX).
	var ipRestartApplied []reconcile.Change
	var ironProxyRevived bool
	var stoppedByIronProxy bool
	if len(resp.AppliedIronProxy) > 0 {
		ipReq := serviceapi.VMApplyIronProxyRequest{
			Name:      cfg.Project.Name,
			Allowlist: docker.EffectiveAllowlist(cfg),
			Secrets:   bindings,
		}
		ipResp, err := client.ApplyIronProxy(context.Background(), ipReq)
		if err != nil {
			return -1, ReconcileResult{}, fmt.Errorf("apply iron-proxy: %w", err)
		}
		ipRestartApplied = resp.AppliedIronProxy
		ironProxyRevived = ipResp.Revived
		stoppedByIronProxy = !ipResp.VMRunning
	}

	sandboxState := resp.SandboxState
	if stoppedByIronProxy {
		sandboxState = "stopped" // so the formatter says "Recorded"
	}

	res := ReconcileResult{
		Rendered:         true,
		SandboxState:     sandboxState,
		Sandbox:          cfg.Project.Name,
		Applied:          resp.Applied,
		AppliedIronProxy: ipRestartApplied,
		IronProxyRevived: ironProxyRevived,
		RecreateRequired: resp.TeardownRequired,
	}

	if len(res.RecreateRequired) == 0 {
		switch {
		case len(res.Applied) > 0 || len(res.AppliedIronProxy) > 0:
			res.NextAction = "applied"
		default:
			res.NextAction = "nothing_to_do"
		}
		return 0, res, nil
	}

	res.Flavor = reconcile.RecreateFlavor(res.RecreateRequired)
	// Surface sessions so the caller can show them in the prompt / JSON
	// output. Best-effort; failure here is non-fatal.
	res.Sessions = probeSessions(tr, cfg.Project.Name)
	res.NextAction = "needs_approval"
	return 0, res, nil
}
