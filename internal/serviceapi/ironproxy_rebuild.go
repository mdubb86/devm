package serviceapi

import (
	"fmt"
	"path/filepath"

	"github.com/mdubb86/devm/internal/docker"
	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/secret"
)

// rebuildIronProxyConfig reconstructs the full spawn config for a
// project's iron-proxy from daemon-readable state: listener ports from
// the on-disk config, allowlist + secret refs from the state snapshot's
// cfg, and secret VALUES from the on-disk secret store. The daemon can
// do this entirely on its own. macCwd is the project's persisted
// WorkspaceHostPath — needed to re-derive a top-level repo's URL when
// repo.url is nil, exactly as the CLI's cold-start path does.
func rebuildIronProxyConfig(cfg identity.Config, projectID string, snapCfg schema.Config, macCwd string) (IronProxyConfig, error) {
	cfgPath, err := IronProxyConfigPath(cfg, projectID)
	if err != nil {
		return IronProxyConfig{}, fmt.Errorf("resolve config path: %w", err)
	}
	diskInfo, err := loadIronProxyInfoFromConfig(cfgPath)
	if err != nil {
		return IronProxyConfig{}, fmt.Errorf("load prior config: %w", err)
	}
	caDir, err := EnsureRuntimeDir(cfg)
	if err != nil {
		return IronProxyConfig{}, fmt.Errorf("runtime dir: %w", err)
	}
	bindings, err := ResolveSecretBindings(snapCfg, secret.NewFileBackend(cfg.SecretsDir()), macCwd)
	if err != nil {
		return IronProxyConfig{}, fmt.Errorf("resolve secrets: %w", err)
	}
	secrets := make([]IronSecret, 0, len(bindings))
	for _, b := range bindings {
		secrets = append(secrets, IronSecret{Name: b.Name, Value: b.Value, Hosts: b.Hosts})
	}
	repoHosts, err := RepoHosts(snapCfg, macCwd)
	if err != nil {
		return IronProxyConfig{}, fmt.Errorf("resolve repo hosts: %w", err)
	}
	return IronProxyConfig{
		HTTPListen:   ironProxyListenAddr(diskInfo.HTTPPort),
		HTTPSListen:  ironProxyListenAddr(diskInfo.HTTPSPort),
		TunnelListen: ironProxyListenAddr(diskInfo.TunnelPort),
		DNSListen:    ironProxyListenAddr(diskInfo.DNSPort),
		DNSProxyIP:   interceptedEgressIP,
		CACertPath:   filepath.Join(caDir, "ca", "root.crt"),
		CAKeyPath:    filepath.Join(caDir, "ca", "root.key"),
		AllowList:    AppendUniqueHosts(docker.EffectiveAllowlist(snapCfg), repoHosts),
		Secrets:      secrets,
	}, nil
}
