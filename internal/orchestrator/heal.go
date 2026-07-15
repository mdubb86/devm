package orchestrator

import (
	"context"

	"github.com/mdubb86/devm/internal/docker"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/secret"
	"github.com/mdubb86/devm/internal/serviceapi"
)

// HealIronProxy resolves the project's current allowlist + secrets and
// asks the daemon to (re)apply iron-proxy — the daemon's heal primitive.
// Called by daemon-touching CLI commands when a /handshake round-trip
// reports the project's iron-proxy as missing or stale.
//
// Lives in orchestrator (not cmd/devm) because resolveSecretBindings is
// unexported here.
func HealIronProxy(ctx context.Context, cfg schema.Config) error {
	bindings, err := resolveSecretBindings(cfg, secret.NewMacKeychain())
	if err != nil {
		return err
	}
	_, err = serviceapi.NewClient().ApplyIronProxy(ctx, serviceapi.VMApplyIronProxyRequest{
		ProjectID: cfg.Project.ID,
		Allowlist: docker.EffectiveAllowlist(cfg), // NOT cfg.Network.Domains() — keep Docker Hub hosts
		Secrets:   bindings,
	})
	return err
}
