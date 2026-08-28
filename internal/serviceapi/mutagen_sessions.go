package serviceapi

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/mdubb86/devm/internal/daemonlog"
	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/mutagen"
	"github.com/mdubb86/devm/internal/repohelpers"
	"github.com/mdubb86/devm/internal/schema"
)

// SessionRepoInfo carries the git provenance for one mirrored repo
// entity: the clone URL and the secret-store entry authenticating it.
// nil on a SessionEntity means the entity is a pure volume with no
// cold-start hydration.
type SessionRepoInfo struct {
	URL    string
	Secret string
}

// SessionEntity is one mirrored path — a repo or a volume — that gets
// its own mutagen sync session. MacMirrorPath is resolved by
// SetupPhase (BuildEntities doesn't know the project's runtime
// identity) and is populated in place as each entity is set up.
type SessionEntity struct {
	Label         string
	GuestPath     string
	MacMirrorPath string
	UserIgnore    []string
	Repo          *SessionRepoInfo
}

// guestHomeDir is the guest-side parent for every repo clone.
const guestHomeDir = "/home/devm"

// SessionName returns the mutagen sync session name for one entity of
// projectID: "devm-<projectID>-<label>".
func SessionName(projectID, label string) string {
	return fmt.Sprintf("devm-%s-%s", projectID, label)
}

// SessionNamePrefix returns the name prefix shared by every session
// belonging to projectID: "devm-<projectID>-".
func SessionNamePrefix(projectID string) string {
	return fmt.Sprintf("devm-%s-", projectID)
}

// findPrimaryRepoName returns the name of cfg.Repos' primary entry:
// the one explicitly marked Primary, or else the sole entry with a nil
// URL. Mirrors schema's validateRepos primary-determination — callers
// are expected to hand BuildEntities an already-validated Config, so
// the "no primary" / "ambiguous primary" cases validateRepos rejects
// are not re-diagnosed here. Returns "" if no entry qualifies.
func findPrimaryRepoName(cfg *schema.Config) string {
	var urlNilName string
	urlNilCount := 0
	for name, r := range cfg.Repos {
		if r.Primary != nil && *r.Primary {
			return name
		}
		if r.URL == nil {
			urlNilName = name
			urlNilCount++
		}
	}
	if urlNilCount == 1 {
		return urlNilName
	}
	return ""
}

// resolveRepoLabel resolves one repos.<name> entry's mutagen sync
// label: an explicit `label:` always wins; else a repo with a URL uses
// schema.BareCloneName; else (the URL-nil primary) the basename of
// macCwd.
func resolveRepoLabel(r schema.RepoConfig, macCwd string) string {
	if r.Label != nil {
		return *r.Label
	}
	if r.URL != nil {
		return schema.BareCloneName(*r.URL)
	}
	return filepath.Base(macCwd)
}

// resolveVolumeLabel resolves one volumes.<name> entry's mutagen sync
// label: an explicit `label:` always wins; else the leaf dir of Path.
func resolveVolumeLabel(v schema.Volume) string {
	if v.Label != nil {
		return *v.Label
	}
	return filepath.Base(v.Path)
}

// PrimaryGuestPath returns the guest-side path of cfg's primary
// mirrored repo (e.g. "/home/devm/<label>"), or "" if cfg declares no
// repos at all. macCwd resolves the URL-nil/label-nil primary's label
// exactly like BuildEntities does. Exported for cmd/devm/pop.go's
// project-root-relative resolution, which needs the primary's guest
// path without needing BuildEntities' full entity list (and the
// git/clone-URL derivation that comes with it for other repos).
func PrimaryGuestPath(cfg *schema.Config, macCwd string) string {
	name := findPrimaryRepoName(cfg)
	if name == "" {
		return ""
	}
	label := resolveRepoLabel(cfg.Repos[name], macCwd)
	return filepath.Join(guestHomeDir, label)
}

// BuildEntities enumerates every mirrored entity in cfg: the primary
// repo (included unless explicitly `volume: false`, which schema
// validation already rejects), every secondary repo that opts in with
// `volume: true`, and every volumes.<name> entry (always mirrored).
// macCwd resolves both the URL-nil primary's label and, when needed,
// its clone URL via `git remote get-url origin`.
func BuildEntities(cfg *schema.Config, macCwd string) ([]SessionEntity, error) {
	primaryName := findPrimaryRepoName(cfg)

	var entities []SessionEntity

	repoNames := make([]string, 0, len(cfg.Repos))
	for name := range cfg.Repos {
		repoNames = append(repoNames, name)
	}
	sort.Strings(repoNames)

	for _, name := range repoNames {
		r := cfg.Repos[name]
		isPrimary := name == primaryName

		var included bool
		if isPrimary {
			included = r.Volume == nil || *r.Volume
		} else {
			included = r.Volume != nil && *r.Volume
		}
		if !included {
			continue
		}

		label := resolveRepoLabel(r, macCwd)

		url := ""
		if r.URL != nil {
			url = *r.URL
		} else if isPrimary {
			derived, err := repohelpers.DeriveRepoURL(macCwd)
			if err != nil {
				return nil, fmt.Errorf("repos.%s: %w", name, err)
			}
			url = derived
		}

		entities = append(entities, SessionEntity{
			Label:      label,
			GuestPath:  filepath.Join(guestHomeDir, label),
			UserIgnore: r.Ignore,
			Repo:       &SessionRepoInfo{URL: url, Secret: r.Secret},
		})
	}

	volNames := make([]string, 0, len(cfg.Volumes))
	for name := range cfg.Volumes {
		volNames = append(volNames, name)
	}
	sort.Strings(volNames)

	for _, name := range volNames {
		v := cfg.Volumes[name]
		entities = append(entities, SessionEntity{
			Label:      resolveVolumeLabel(v),
			GuestPath:  v.Path,
			UserIgnore: v.Ignore,
		})
	}

	return entities, nil
}

// ensureGuestDirScript builds the exec script that guarantees
// guestPath exists and is owned by devm: an `install -d` as root
// creates the full parent chain with devm ownership, then a `mkdir -p`
// as devm creates (or confirms) the leaf itself.
func ensureGuestDirScript(guestPath string) string {
	parent := filepath.Dir(guestPath)
	return fmt.Sprintf(
		"sudo install -d -o devm -g devm %s\nsudo -u devm mkdir -p %s\n",
		PosixShellQuote(parent),
		PosixShellQuote(guestPath),
	)
}

// SetupPhase brings every entity's mutagen sync session up to date:
// ensures both sides' mirror dirs exist, cold-start clones a repo
// entity into an all-empty guest, verifies the in-sync guard before
// touching an existing target, then creates a fresh session or resumes
// a paused one. The VM is left running on a guard rejection — the
// caller decides whether to surface it to the user and retry.
func SetupPhase(
	ctx context.Context,
	cli *mutagen.CLI,
	cfg identity.Config,
	projectID string,
	entities []SessionEntity,
	exec GuestExec,
	guestSSHTarget, ironProxyURL, guestCACertPath string,
) error {
	for i := range entities {
		e := &entities[i]

		macMirror, _, err := ensureMirrorDir(cfg, projectID, e.Label)
		if err != nil {
			return fmt.Errorf("mutagen setup %s: ensure mac mirror: %w", e.Label, err)
		}
		e.MacMirrorPath = macMirror

		if _, stderr, exitCode, err := exec(ensureGuestDirScript(e.GuestPath)); err != nil {
			return fmt.Errorf("mutagen setup %s: ensure guest dir: %w", e.Label, err)
		} else if exitCode != 0 {
			return fmt.Errorf("mutagen setup %s: ensure guest dir: exit %d: %s", e.Label, exitCode, stderr)
		}

		if e.Repo != nil {
			macSide, err := ScanMac(macMirror)
			if err != nil {
				return fmt.Errorf("mutagen setup %s: %w", e.Label, err)
			}
			guestSide, err := ScanGuest(exec, e.GuestPath)
			if err != nil {
				return fmt.Errorf("mutagen setup %s: %w", e.Label, err)
			}
			if macSide.Count == 0 && guestSide.Count == 0 {
				if err := CloneRepoInGuest(exec, CloneRequest{
					URL:             e.Repo.URL,
					SecretName:      e.Repo.Secret,
					GuestTargetPath: e.GuestPath,
					IronProxyURL:    ironProxyURL,
					GuestCACertPath: guestCACertPath,
				}); err != nil {
					return fmt.Errorf("mutagen setup %s: %w", e.Label, err)
				}
			}
		}

		macSide, err := ScanMac(macMirror)
		if err != nil {
			return fmt.Errorf("mutagen setup %s: %w", e.Label, err)
		}
		guestSide, err := ScanGuest(exec, e.GuestPath)
		if err != nil {
			return fmt.Errorf("mutagen setup %s: %w", e.Label, err)
		}

		verdict := GuardCheck(macSide, guestSide)
		if !verdict.OK {
			daemonlog.Errorf("mutagen: guard rejected %s: %s", e.Label, verdict.Reason)
			return fmt.Errorf("in-sync guard failed for %s: %s", e.Label, verdict.Reason)
		}

		name := SessionName(projectID, e.Label)
		sessions, err := cli.SyncList(name)
		if err != nil {
			return fmt.Errorf("mutagen setup %s: list sessions: %w", e.Label, err)
		}
		var existing *mutagen.SyncSession
		for j := range sessions {
			if sessions[j].Name == name {
				existing = &sessions[j]
				break
			}
		}

		if existing != nil {
			if existing.Status == "paused" {
				if err := cli.SyncResume(existing.ID); err != nil {
					return fmt.Errorf("mutagen setup %s: resume session: %w", e.Label, err)
				}
			}
			continue
		}

		sessionCfg := mutagen.ComposeConfig(e.UserIgnore)
		configPath := mutagen.ConfigFilePath(mutagenSessionsDir(cfg), projectID, e.Label)
		if err := mutagen.WriteConfigFile(configPath, sessionCfg); err != nil {
			return fmt.Errorf("mutagen setup %s: write session config: %w", e.Label, err)
		}

		beta := "devm@" + guestSSHTarget + ":" + e.GuestPath
		if _, err := cli.SyncCreate(name, macMirror, beta, configPath, nil); err != nil {
			return fmt.Errorf("mutagen setup %s: create session: %w", e.Label, err)
		}
	}
	return nil
}

// StopPhase flushes and pauses every mutagen session belonging to
// projectID, best-effort: a failure on one session is logged and does
// not block the others — mutagen's own journal handles a crash
// mid-flush on the next resume.
func StopPhase(cli *mutagen.CLI, projectID string) error {
	sessions, err := cli.SyncList(SessionNamePrefix(projectID))
	if err != nil {
		return fmt.Errorf("mutagen stop %s: list sessions: %w", projectID, err)
	}
	for _, s := range sessions {
		if err := cli.SyncFlush(s.ID); err != nil {
			daemonlog.Errorf("mutagen stop %s: flush session %s: %v", projectID, s.Name, err)
		}
		if err := cli.SyncPause(s.ID); err != nil {
			daemonlog.Errorf("mutagen stop %s: pause session %s: %v", projectID, s.Name, err)
		}
	}
	return nil
}

// TeardownPhase permanently terminates every mutagen session belonging
// to projectID, best-effort.
func TeardownPhase(cli *mutagen.CLI, projectID string) error {
	sessions, err := cli.SyncList(SessionNamePrefix(projectID))
	if err != nil {
		return fmt.Errorf("mutagen teardown %s: list sessions: %w", projectID, err)
	}
	for _, s := range sessions {
		if err := cli.SyncTerminate(s.ID); err != nil {
			daemonlog.Errorf("mutagen teardown %s: terminate session %s: %v", projectID, s.Name, err)
		}
	}
	return nil
}
