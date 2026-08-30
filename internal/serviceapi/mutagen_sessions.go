package serviceapi

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

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
// SetupVolumesPhase (BuildEntities doesn't know the project's runtime
// identity) and is populated in place as each entity is set up.
type SessionEntity struct {
	Label         string
	GuestPath     string
	MacMirrorPath string
	UserIgnore    []string
	Repo          *SessionRepoInfo

	// NoMirror marks a secondary repo declared without `volume: true`:
	// it gets a cold-start clone into the guest but no Mac mirror dir
	// and no mutagen sync session. Always false for the primary repo
	// and for volumes.<name> entries.
	NoMirror bool
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

// BuildEntities enumerates every repo/volume entity in cfg: the
// primary repo, every secondary repo, and every volumes.<name> entry.
// A secondary repo without `volume: true` comes back with NoMirror
// set — it still gets cold-start cloned into the guest (by
// SetupReposPhase), but SetupVolumesPhase skips the Mac mirror dir and
// mutagen sync session for it. macCwd
// resolves both the URL-nil primary's label and, when needed, its
// clone URL via `git remote get-url origin`.
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
		noMirror := !isPrimary && !(r.Volume != nil && *r.Volume)

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
			NoMirror:   noMirror,
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

// cloneRepoInGuestFn is the test seam for CloneRepoInGuest — production
// always dispatches to CloneRepoInGuest itself.
var cloneRepoInGuestFn = CloneRepoInGuest

// coldStartCloneOnly handles a NoMirror entity: ensure the guest dir
// exists, then clone into it if it's empty. No Mac mirror dir, no
// guard check, no mutagen session — the guest clone is the entity's
// only state. Used by SetupReposPhase.
func coldStartCloneOnly(exec GuestExec, e *SessionEntity, ironProxyURL, guestCACertPath string) error {
	if _, stderr, exitCode, err := exec(ensureGuestDirScript(e.GuestPath)); err != nil {
		return fmt.Errorf("mutagen setup %s: ensure guest dir: %w", e.Label, err)
	} else if exitCode != 0 {
		return fmt.Errorf("mutagen setup %s: ensure guest dir: exit %d: %s", e.Label, exitCode, stderr)
	}

	guestSide, err := ScanGuest(exec, e.GuestPath)
	if err != nil {
		return fmt.Errorf("mutagen setup %s: %w", e.Label, err)
	}
	if guestSide.Count != 0 {
		return nil
	}

	if err := cloneRepoInGuestFn(exec, CloneRequest{
		URL:             e.Repo.URL,
		SecretName:      e.Repo.Secret,
		GuestTargetPath: e.GuestPath,
		IronProxyURL:    ironProxyURL,
		GuestCACertPath: guestCACertPath,
	}); err != nil {
		return fmt.Errorf("mutagen setup %s: %w", e.Label, err)
	}
	return nil
}

// cloneOneRepoIfEmpty runs a guest git clone for one mirrored repo
// entity iff both the Mac mirror and the guest side are currently
// empty. A pure volume (Repo == nil) is a no-op. Used by
// SetupReposPhase for every entity that isn't NoMirror (NoMirror
// entities go through coldStartCloneOnly instead, since they have no
// Mac mirror side to scan).
func cloneOneRepoIfEmpty(cfg identity.Config, projectID string, e SessionEntity, exec GuestExec, ironProxyURL, guestCACertPath string) error {
	if e.Repo == nil {
		return nil
	}

	macMirror, _, err := ensureMirrorDir(cfg, projectID, e.Label)
	if err != nil {
		return fmt.Errorf("mutagen setup %s: ensure mac mirror: %w", e.Label, err)
	}
	macSide, err := ScanMac(macMirror)
	if err != nil {
		return fmt.Errorf("mutagen setup %s: %w", e.Label, err)
	}
	guestSide, err := ScanGuest(exec, e.GuestPath)
	if err != nil {
		return fmt.Errorf("mutagen setup %s: %w", e.Label, err)
	}
	if macSide.Count > 0 || guestSide.Count > 0 {
		return nil
	}

	if err := cloneRepoInGuestFn(exec, CloneRequest{
		URL:             e.Repo.URL,
		SecretName:      e.Repo.Secret,
		GuestTargetPath: e.GuestPath,
		IronProxyURL:    ironProxyURL,
		GuestCACertPath: guestCACertPath,
	}); err != nil {
		return fmt.Errorf("mutagen setup %s: %w", e.Label, err)
	}
	return nil
}

// SetupReposPhase runs a cold-start git clone for each repo entity
// where the relevant sides are empty: a NoMirror entity clones iff the
// guest is empty; a mirrored repo entity clones iff both the Mac
// mirror and the guest are empty. A pure volume entity is a no-op. No
// session work happens here — the mutagen sessions SetupVolumesPhase
// already established pick up the new guest content on their own.
// Fast no-op when entities has no repos needing a clone.
func SetupReposPhase(ctx context.Context, cfg identity.Config, projectID string, entities []SessionEntity, exec GuestExec, ironProxyURL, guestCACertPath string) error {
	for i := range entities {
		e := &entities[i]
		if e.Repo == nil {
			continue
		}
		if e.NoMirror {
			if err := coldStartCloneOnly(exec, e, ironProxyURL, guestCACertPath); err != nil {
				return err
			}
			continue
		}
		if err := cloneOneRepoIfEmpty(cfg, projectID, *e, exec, ironProxyURL, guestCACertPath); err != nil {
			return err
		}
	}
	return nil
}

// verifyGitHEAD runs `git -C <macMirrorPath> rev-parse --verify HEAD`
// and returns a non-nil error whose text carries git's stderr on any
// failure — truncated .git, missing HEAD object, or not-a-git-repo all
// surface the same way.
func verifyGitHEAD(macMirrorPath string) error {
	cmd := exec.Command("git", "-C", macMirrorPath, "rev-parse", "--verify", "HEAD")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(stderr.String()))
	}
	return nil
}

// SetupVolumesPhase establishes a mutagen sync session for every
// entity (volumes and repos alike). Uniform per-entity code path;
// clone-if-empty for repos is handled separately by SetupReposPhase.
// It ensures both sides' mirror dirs exist, verifies the in-sync guard
// before touching an existing target, then creates a fresh session or
// resumes a paused one. NoMirror entities are skipped entirely — they
// never get a Mac mirror dir or a mutagen session. The VM is left
// running on a guard rejection — the caller decides whether to
// surface it to the user and retry.
func SetupVolumesPhase(
	ctx context.Context,
	cli *mutagen.CLI,
	cfg identity.Config,
	projectID string,
	entities []SessionEntity,
	exec GuestExec,
	guestSSHTarget string,
) error {
	for i := range entities {
		e := &entities[i]

		if e.NoMirror {
			continue
		}

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

		// Warm-attach branch: an existing session for this label already
		// exists in mutagen's state, so this label is not a first-time
		// setup — no guard check. Resume if paused (see contract 04 + 05
		// for the .Paused vs .Status semantics) and continue. Skipping
		// the guard here matters: between `devm stop` (pause) and
		// `devm start` (resume), transient content on either side can
		// nudge the entry counts out of alignment for the flush that
		// hasn't caught up yet — the guard is only there to protect a
		// FRESH session-create from silently destroying content, which
		// is not the situation on resume.
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
			if existing.Paused {
				if err := cli.SyncResume(existing.ID); err != nil {
					return fmt.Errorf("mutagen setup %s: resume session: %w", e.Label, err)
				}
			}
			continue
		}

		// Cold-start branch: no session exists yet for this label. Run
		// the guard before we let mutagen touch the pair — an
		// all-empty pair (the common case, before SetupReposPhase has
		// run its clone) passes trivially, since GuardCheck treats
		// either side being empty as never conflicting.
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

		// Integrity gate: a repo entity's persistent Mac mirror must be
		// a healthy git checkout before mutagen ever touches it — a
		// corrupt mirror (truncated .git, missing HEAD object) passed
		// to sync-create would propagate straight into the guest.
		if e.Repo != nil && macSide.Count > 0 {
			if err := verifyGitHEAD(e.MacMirrorPath); err != nil {
				return fmt.Errorf(
					"repo %s: mac mirror at %s failed integrity check "+
						"(git rev-parse --verify HEAD: %s) — the persistent checkout "+
						"appears corrupt; inspect the directory or `devm volume rm %s` "+
						"to force a fresh clone on next `devm start`",
					e.Label, e.MacMirrorPath, err, e.Label,
				)
			}
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
		// Skip flush on an already-paused session — `mutagen sync flush`
		// errors "session is paused" and spams the daemon error log with
		// no upside (flush on a paused session couldn't do anything
		// anyway). Pinned in e2e/test_mutagen_contract_10.
		if !s.Paused {
			if err := cli.SyncFlush(s.ID); err != nil {
				daemonlog.Errorf("mutagen stop %s: flush session %s: %v", projectID, s.Name, err)
			}
		}
		if err := cli.SyncPause(s.ID); err != nil {
			daemonlog.Errorf("mutagen stop %s: pause session %s: %v", projectID, s.Name, err)
		}
	}
	return nil
}

// FlushAll blocks until every non-paused mutagen sync session belonging to
// projectID has completed its current sync cycle. Called by the orchestrator's
// RunStartupCommands phase to guarantee the workspace is hydrated (both
// directions) before a startup command reads from it.
//
// Fail-fast: returns the first non-nil SyncFlush error. A startup command
// racing an unflushed entity would read a partial workspace anyway.
// Paused sessions are skipped (mirrors StopPhase's rationale: `mutagen sync
// flush` on a paused session errors "session is paused" with no upside).
func FlushAll(cli *mutagen.CLI, projectID string) error {
	sessions, err := cli.SyncList(SessionNamePrefix(projectID))
	if err != nil {
		return fmt.Errorf("mutagen flush %s: list sessions: %w", projectID, err)
	}
	for _, s := range sessions {
		if s.Paused {
			continue
		}
		if err := cli.SyncFlush(s.ID); err != nil {
			return fmt.Errorf("mutagen flush %s: flush %s: %w", projectID, s.Name, err)
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
