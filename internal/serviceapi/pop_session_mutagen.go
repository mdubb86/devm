package serviceapi

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/mutagen"
)

func popSessionSyncConfig(kind PopKind, targetName string) mutagen.SessionConfig {
	cfg := mutagen.SessionConfig{
		SyncMode:  "one-way-safe",
		ScanMode:  "accelerated",
		VCSIgnore: false,
	}
	if kind == PopKindFile {
		cfg.Ignores = []string{"**", "!" + targetName}
	}
	return cfg
}

func popSessionConfigPath(cfg identity.Config, projectName, sessionID string) string {
	return filepath.Join(mutagenSessionsDir(cfg), projectName, "pop-"+sessionID+".yml")
}

// CreatePopSyncSession creates the Mac scratch dir, writes the per-
// session mutagen config yaml, and calls `mutagen sync create` with a
// one-way-safe session from the guest to that Mac dir. On success,
// ps.MutagenSessionID is populated with the id mutagen returned.
func CreatePopSyncSession(cli *mutagen.CLI, cfg identity.Config, guestSSHTarget string, ps *PopSession) error {
	if err := os.MkdirAll(ps.MacDir, 0755); err != nil {
		return fmt.Errorf("pop session %s: mkdir mac dir: %w", ps.ID, err)
	}
	configPath := popSessionConfigPath(cfg, ps.ProjectName, ps.ID)
	if err := mutagen.WriteConfigFile(configPath, popSessionSyncConfig(ps.Kind, ps.TargetName)); err != nil {
		return fmt.Errorf("pop session %s: write config: %w", ps.ID, err)
	}
	alphaPath := ps.GuestPath
	if ps.Kind == PopKindFile {
		alphaPath = filepath.Dir(ps.GuestPath)
	}
	alpha := "devm@" + guestSSHTarget + ":" + alphaPath
	beta := ps.MacDir
	name := popSessionMutagenName(ps.ProjectName, ps.ID)
	id, err := cli.SyncCreate(name, alpha, beta, configPath, nil)
	if err != nil {
		return fmt.Errorf("pop session %s: create sync: %w", ps.ID, err)
	}
	ps.MutagenSessionID = id
	return nil
}

// TearDownPopSyncSession terminates the mutagen session (best-effort),
// removes the Mac scratch dir, and deletes the session config yaml.
// Attempts all three steps unconditionally; returns the first error
// encountered.
func TearDownPopSyncSession(cli *mutagen.CLI, cfg identity.Config, ps PopSession) error {
	var firstErr error
	if ps.MutagenSessionID != "" {
		if err := cli.SyncTerminate(ps.MutagenSessionID); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("pop session %s: terminate: %w", ps.ID, err)
		}
	}
	if err := os.RemoveAll(ps.MacDir); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("pop session %s: rm mac dir: %w", ps.ID, err)
	}
	if err := os.Remove(popSessionConfigPath(cfg, ps.ProjectName, ps.ID)); err != nil && !os.IsNotExist(err) && firstErr == nil {
		firstErr = fmt.Errorf("pop session %s: rm config: %w", ps.ID, err)
	}
	return firstErr
}
