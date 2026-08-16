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
// do this entirely on its own.
func rebuildIronProxyConfig(cfg identity.Config, projectID string, snapCfg schema.Config) (IronProxyConfig, error) {
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
	bindings, err := ResolveSecretBindings(snapCfg, secret.NewFileBackend(cfg.SecretsDir()))
	if err != nil {
		return IronProxyConfig{}, fmt.Errorf("resolve secrets: %w", err)
	}
	secrets := make([]IronSecret, 0, len(bindings))
	for _, b := range bindings {
		secrets = append(secrets, IronSecret{Name: b.Name, Value: b.Value, Hosts: b.Hosts})
	}
	return IronProxyConfig{
		HTTPListen:  ironProxyListenAddr(diskInfo.HTTPPort),
		HTTPSListen: ironProxyListenAddr(diskInfo.HTTPSPort),
		DNSListen:   ironProxyListenAddr(diskInfo.DNSPort),
		DNSProxyIP:  interceptedEgressIP,
		CACertPath:  filepath.Join(caDir, "ca", "root.crt"),
		CAKeyPath:   filepath.Join(caDir, "ca", "root.key"),
		AllowList:   docker.EffectiveAllowlist(snapCfg),
		Secrets:     secrets,
	}, nil
}
