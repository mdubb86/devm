package snapshot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/schema"
)

// RestoreConfig carries everything Restore needs to feed one archive
// back into the guest. HookRunner and Entries are required. TS empty
// means "resolve via latest symlink".
//
// PreTimeout / StepTimeout / PostTimeout each bound only their own
// phase's wall time, each derived fresh from the caller's context so
// a step timeout does not cancel the post-finalizer. Stream mode uses
// StepTimeout only — pre and post have no meaning there.
//
// Path selects the mode. Nil ⇒ stream mode (HookRunner.RunRestore
// reads the archive bytes). Non-nil ⇒ path mode: the optional
// pre_restore_<name> hook runs first, then TarRunner pipes the
// archive bytes into tar `-x` at that guest path, then the optional
// post_restore_<name> hook runs. TarRunner is required when Path is
// set. The handler resolves Path from the yaml entry's `path:` at
// dispatch time.
type RestoreConfig struct {
	Ident       identity.Config
	ProjectID   string
	EntryName   string
	TS          string
	PreTimeout  time.Duration
	StepTimeout time.Duration
	PostTimeout time.Duration
	HookRunner  HookRunner
	TarRunner   TarRunner
	Path        *string
	// Entries is the current declared snapshots map from devm.yaml.
	// Restore refuses to feed an archive into a hook that no longer
	// exists — a `restore_<name>` function the config has removed
	// would either be undefined or (worse) now mean something else.
	Entries schema.OrderedMap[schema.Snapshot]
	// Force bypasses mode/path drift refusals. Set from the --force
	// CLI flag so an operator can replay an archive after rewriting
	// the yaml entry without first running `devm snapshot clear`.
	Force bool
}

// Restore acquires the per-entry flock, resolves the archive, opens
// it, wraps it in a zstd decoder, and hands the decoded stream to
// HookRunner.RunRestore as the hook's stdin. The hook is responsible
// for whatever recovery it performs inside the guest; Restore is
// non-transactional and does no rollback on failure.
//
// The boolean return names what happened: true means an archive was
// found and replayed, false with a nil error means there was nothing
// to restore (the latest symlink was absent). First-boot callers that
// hook into install() rely on this so the hook does not fail before
// the state it would restore has ever been captured. A non-nil error
// is a real failure — a missing archive the caller asked for by ts,
// decode error, non-zero hook exit, drift refusal, orphan entry — and
// the caller handles it.
//
// Nothing is written to failures.log here — a restore failure
// surfaces to the caller directly; the capture failure log is about
// retaining signal across potential data loss on write.
func Restore(ctx context.Context, cfg RestoreConfig) (bool, error) {
	if cfg.HookRunner == nil {
		return false, errors.New("snapshot: restore: HookRunner is required")
	}
	if cfg.Path != nil && cfg.TarRunner == nil {
		return false, errors.New("snapshot: restore: TarRunner is required for path mode")
	}
	dir := EntryDir(cfg.Ident, cfg.ProjectID, cfg.EntryName)

	// Orphan check runs before the lock so operators get a fast,
	// precise error instead of blocking on a lock for a doomed call.
	snap, ok := cfg.Entries.Get(cfg.EntryName)
	if !ok {
		return false, fmt.Errorf(
			"snapshot: restore: entry %q is not declared in devm.yaml; inspect %s manually",
			cfg.EntryName, dir)
	}

	// Resolve ts and screen for mode/path drift before the flock so
	// operators get a fast, precise error instead of blocking on a
	// lock for a doomed call.
	ts := cfg.TS
	if ts == "" {
		latest, err := LatestTS(dir)
		if err != nil {
			return false, fmt.Errorf("snapshot: restore: resolve latest: %w", err)
		}
		if latest == "" {
			return false, nil
		}
		ts = latest
	}
	meta, err := ReadMeta(dir, ts)
	if err != nil {
		return false, fmt.Errorf("snapshot: restore: read meta %s: %w", ts, err)
	}
	if err := checkRestoreDrift(cfg, snap, meta); err != nil {
		return false, err
	}

	unlock, err := acquireLock(ctx, dir)
	if err != nil {
		return false, err
	}
	defer unlock()

	archive := filepath.Join(dir, ts+".zst")
	f, err := os.Open(archive)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, fmt.Errorf("snapshot: restore: archive %s not found", archive)
		}
		return false, fmt.Errorf("snapshot: restore: open archive %s: %w", archive, err)
	}
	defer f.Close()

	zr, err := zstd.NewReader(f)
	if err != nil {
		return false, fmt.Errorf("snapshot: restore: zstd reader: %w", err)
	}
	defer zr.Close()

	if cfg.Path != nil {
		if err := runPathModeRestore(ctx, cfg, meta, zr); err != nil {
			return false, err
		}
		return true, nil
	}

	// Stream mode uses the existing bytes-in-bytes-out contract;
	// checkRestoreDrift already refused when Meta.Mode indicates a
	// path-mode archive. The step bound is the only phase timeout
	// that applies to stream mode.
	stepCtx, cancel := phaseCtx(ctx, cfg.StepTimeout)
	defer cancel()
	exitCode, runErr := cfg.HookRunner.RunRestore(stepCtx, cfg.EntryName, zr)
	if runErr != nil {
		return false, fmt.Errorf("snapshot: restore: hook: %w", runErr)
	}
	if exitCode != 0 {
		return false, fmt.Errorf("snapshot: restore: hook exit %d", exitCode)
	}
	return true, nil
}

// runPathModeRestore runs the path-mode restore pipeline: look up
// which lifecycle hooks exist, invoke pre_restore_<name> when present,
// hand the decoded stream to TarRunner.RunRestoreTar on success, and
// always invoke post_restore_<name> when present (finalizer). The
// in-guest shell wrapper around tar `-x` handles mkdir -p for the
// extract target's parent — nothing on the Mac side needs to touch
// the guest filesystem. Each phase derives its own context from
// callerCtx bounded by that phase's timeout — a step timeout firing
// cannot cancel the post-restore finalizer. On any failure, aggregate
// all phase errors with errors.Join so each stays unwrappable through
// errors.Is / errors.As.
func runPathModeRestore(callerCtx context.Context, cfg RestoreConfig, meta Meta, stream io.Reader) error {
	// HookExists is step preamble — share the step bound.
	existsCtx, cancelExists := phaseCtx(callerCtx, cfg.StepTimeout)
	presence, hookErr := cfg.HookRunner.HookExists(existsCtx, cfg.EntryName)
	cancelExists()
	if hookErr != nil {
		return fmt.Errorf("snapshot restore %s: check hooks: %w", cfg.EntryName, hookErr)
	}

	env := map[string]string{
		"DEVM_SNAPSHOT_NAME": cfg.EntryName,
		"DEVM_RESTORE_PATH":  *cfg.Path,
	}
	var errs []error
	var preExit *int

	if presence.PreRestore {
		preCtx, cancelPre := phaseCtx(callerCtx, cfg.PreTimeout)
		code, perr := cfg.HookRunner.RunPreRestore(preCtx, cfg.EntryName, env)
		cancelPre()
		if perr != nil {
			// Transport failure: the hook did not run through to a
			// numeric code, so DEVM_PRE_EXIT_CODE must stay unset in
			// post's env.
			errs = append(errs, fmt.Errorf("pre_restore_%s: %w", cfg.EntryName, perr))
		} else {
			c := code
			preExit = &c
			if code != 0 {
				errs = append(errs, fmt.Errorf("pre_restore_%s exit %d", cfg.EntryName, code))
			}
		}
	}

	if len(errs) == 0 {
		stepCtx, cancelStep := phaseCtx(callerCtx, cfg.StepTimeout)
		code, terr := cfg.TarRunner.RunRestoreTar(stepCtx, cfg.EntryName, *cfg.Path, meta.IsDir, stream)
		cancelStep()
		if terr != nil {
			errs = append(errs, fmt.Errorf("extract %s: %w", cfg.EntryName, terr))
		} else if code != 0 {
			errs = append(errs, fmt.Errorf("extract %s exit %d", cfg.EntryName, code))
		}
	}

	if presence.PostRestore {
		postEnv := map[string]string{
			"DEVM_SNAPSHOT_NAME": cfg.EntryName,
			"DEVM_RESTORED_PATH": *cfg.Path,
		}
		if preExit != nil {
			postEnv["DEVM_PRE_EXIT_CODE"] = strconv.Itoa(*preExit)
		}
		postCtx, cancelPost := phaseCtx(callerCtx, cfg.PostTimeout)
		code, perr := cfg.HookRunner.RunPostRestore(postCtx, cfg.EntryName, postEnv)
		cancelPost()
		if perr != nil {
			errs = append(errs, fmt.Errorf("post_restore_%s: %w", cfg.EntryName, perr))
		} else if code != 0 {
			errs = append(errs, fmt.Errorf("post_restore_%s exit %d", cfg.EntryName, code))
		}
	}

	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}
	// errors.Join keeps each phase error unwrappable — callers using
	// errors.Is / errors.As see through the aggregate to the sentinels
	// a specific phase returned.
	return fmt.Errorf("snapshot restore %s: %w", cfg.EntryName, errors.Join(errs...))
}

// checkRestoreDrift refuses the restore when the archive's recorded
// mode or captured path disagrees with what devm.yaml currently
// declares. cfg.Force bypasses both refusals and logs the override at
// the daemon level so the operator decision is auditable — but only
// when the archive and yaml actually disagree. --force on a
// drift-free restore is a no-op and emits no log line.
//
// Archive mode comes from Meta.EffectiveMode(), which resolves v0.31
// sidecars (empty Mode) to ModeStream. Yaml mode is ModePath when the
// entry declares a path, ModeStream otherwise. The path drift check
// only fires when both archive and yaml are path-mode — a stream-mode
// archive has no CapturedPath to drift against.
func checkRestoreDrift(cfg RestoreConfig, snap schema.Snapshot, meta Meta) error {
	archiveMode := meta.EffectiveMode()
	yamlMode := ModeStream
	if snap.Path != nil {
		yamlMode = ModePath
	}
	modeDrift := archiveMode != yamlMode
	pathDrift := archiveMode == ModePath && snap.Path != nil && meta.CapturedPath != *snap.Path
	if cfg.Force {
		if modeDrift || pathDrift {
			log.Printf("snapshot restore %s: --force bypassing drift (archive=%s, yaml=%s)",
				cfg.EntryName, archiveMode, yamlMode)
		}
		return nil
	}
	if modeDrift {
		return fmt.Errorf("snapshot restore %s: archive is %s-mode; yaml now declares %s. Run 'devm snapshot clear %s' to start fresh, or use --force.",
			cfg.EntryName, archiveMode, yamlMode, cfg.EntryName)
	}
	if pathDrift {
		return fmt.Errorf("snapshot restore %s: archive captured from %s; yaml now says %s. Fix yaml or use --force.",
			cfg.EntryName, meta.CapturedPath, *snap.Path)
	}
	return nil
}
