package serviceapi

import (
	"context"
	"fmt"

	"github.com/mdubb86/devm/internal/daemonlog"
	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/mutagen"
)

// configSyncLabel names the generated config-yaml file for the
// dedicated config-sync session (see mutagen.ConfigFilePath) — distinct
// from any repo/volume entity label so the two never collide on disk.
const configSyncLabel = "config-sync"

// configSyncIgnores ignores everything under <macCwd> except
// devm.yaml and devm.me.yaml: the "*" blanket ignore covers the rest
// of the project's working tree, while the two negations un-ignore
// only the two config files that are meant to cross into the guest.
var configSyncIgnores = []string{"*", "!devm.yaml", "!devm.me.yaml"}

// ConfigSyncSessionName returns the mutagen sync session name for
// projectName's dedicated devm.yaml/devm.me.yaml sync.
func ConfigSyncSessionName(projectName string) string {
	return fmt.Sprintf("devm-config-%s", projectName)
}

// SetupConfigSync creates, idempotently, the bidirectional mutagen
// session syncing <macCwd>/{devm.yaml,devm.me.yaml} with
// /home/devm/{devm.yaml,devm.me.yaml}. A no-op if the session already
// exists — this session is never paused/resumed like the per-entity
// workspace sessions, only created and (on stop) terminated outright,
// so "already exists" is the only warm-attach case there is.
func SetupConfigSync(ctx context.Context, cli *mutagen.CLI, cfg identity.Config, projectName, macCwd string) error {
	name := ConfigSyncSessionName(projectName)

	sessions, err := cli.SyncList(name)
	if err != nil {
		return fmt.Errorf("config sync %s: list sessions: %w", projectName, err)
	}
	for _, s := range sessions {
		if s.Name == name {
			return nil
		}
	}

	if macCwd == "" {
		daemonlog.Errorf("config sync %s: macCwd required", projectName)
		return fmt.Errorf("config sync %s: macCwd required", projectName)
	}

	sessionCfg := mutagen.ComposeConfig(configSyncIgnores)
	configPath := mutagen.ConfigFilePath(mutagenSessionsDir(cfg), projectName, configSyncLabel)
	if err := mutagen.WriteConfigFile(configPath, sessionCfg); err != nil {
		return fmt.Errorf("config sync %s: write session config: %w", projectName, err)
	}

	guestSSHTarget := "devm-" + projectName
	beta := "devm@" + guestSSHTarget + ":" + guestHomeDir
	if _, err := cli.SyncCreate(name, macCwd, beta, configPath, nil); err != nil {
		return fmt.Errorf("config sync %s: create session: %w", projectName, err)
	}
	return nil
}

// StopConfigSync terminates projectName's dedicated config-sync
// session. Idempotent: no matching session is not an error.
func StopConfigSync(ctx context.Context, cli *mutagen.CLI, projectName string) error {
	name := ConfigSyncSessionName(projectName)

	sessions, err := cli.SyncList(name)
	if err != nil {
		return fmt.Errorf("config sync %s: list sessions: %w", projectName, err)
	}
	for _, s := range sessions {
		if s.Name == name {
			if err := cli.SyncTerminate(s.ID); err != nil {
				return fmt.Errorf("config sync %s: terminate session: %w", projectName, err)
			}
			return nil
		}
	}
	return nil
}
