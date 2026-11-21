package snapshot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/mdubb86/devm/internal/identity"
)

// defaultZstdLevel is the compression level the spec starts at. It
// trades CPU for archive size at a point roughly matching postgres's
// own backup-compression defaults.
const defaultZstdLevel = 10

// CaptureConfig carries everything Capture needs to run one backup
// end to end. Nullable knobs (PreTimeout / StepTimeout / PostTimeout,
// ZstdLevel, Keep, Now) use their zero value as "unset" because each
// has a natural default Capture resolves itself; Source and HookRunner
// are required.
//
// PreTimeout / StepTimeout / PostTimeout each bound only their own
// phase's wall time, each derived fresh from the caller's context so
// a step timeout does not cancel the post-finalizer. Stream mode uses
// StepTimeout only — pre and post have no meaning there.
//
// Path selects the mode. Nil ⇒ stream mode (HookRunner.RunBackup
// writes archive bytes directly). Non-nil ⇒ path mode: the optional
// pre_backup_<name> hook runs first, then TarRunner tars the declared
// (or overridden) guest path. TarRunner is required when Path is set.
type CaptureConfig struct {
	Ident       identity.Config
	ProjectID   string
	EntryName   string
	Source      Source
	PreTimeout  time.Duration
	StepTimeout time.Duration
	PostTimeout time.Duration
	ZstdLevel   int
	Keep        int
	Path        *string
	HookRunner  HookRunner
	TarRunner   TarRunner
	Now         func() time.Time
}

// Capture runs one backup hook against the guest, streams its stdout
// through zstd into a Mac-side archive, finalises the archive and
// its meta sidecar, points the latest symlink at it, and prunes
// older archives to cfg.Keep. On any failure path (hook non-zero,
// transport error, ctx deadline) it removes the in-flight tempfile,
// appends a line to failures.log, and returns a zero Meta with the
// failure wrapped.
//
// The flock on <entry>/.lock serializes concurrent callers; Capture
// respects ctx cancellation while waiting for the lock, so a
// lifecycle-path caller that times out never races a long-held
// lock holder.
func Capture(ctx context.Context, cfg CaptureConfig) (Meta, error) {
	if cfg.HookRunner == nil {
		return Meta{}, errors.New("snapshot: capture: HookRunner is required")
	}
	if cfg.Source == "" {
		return Meta{}, errors.New("snapshot: capture: Source is required")
	}
	if cfg.Path != nil && cfg.TarRunner == nil {
		return Meta{}, errors.New("snapshot: capture: TarRunner is required for path mode")
	}

	now := cfg.Now
	if now == nil {
		now = time.Now
	}

	dir := EntryDir(cfg.Ident, cfg.ProjectID, cfg.EntryName)
	if err := EnsureEntryDir(cfg.Ident, cfg.ProjectID, cfg.EntryName); err != nil {
		return Meta{}, err
	}

	unlock, err := acquireLock(ctx, dir)
	if err != nil {
		return Meta{}, err
	}
	defer unlock()

	ts := NewTimestamp(now())

	tmp, err := os.CreateTemp(dir, ts+".*.tmp")
	if err != nil {
		return Meta{}, fmt.Errorf("snapshot: create temp archive: %w", err)
	}
	tmpPath := tmp.Name()

	level := cfg.ZstdLevel
	if level == 0 {
		level = defaultZstdLevel
	}
	zw, err := zstd.NewWriter(tmp, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(level)))
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return Meta{}, fmt.Errorf("snapshot: zstd writer: %w", err)
	}

	var (
		mode         Mode
		capturedPath string
		isDir        bool
		exitCode     int
		runErr       error
		presence     HookPresence
		preExit      *int
	)
	start := now()
	if cfg.Path == nil {
		mode = ModeStream
		stepCtx, cancel := phaseCtx(ctx, cfg.StepTimeout)
		exitCode, runErr = cfg.HookRunner.RunBackup(stepCtx, cfg.EntryName, zw)
		cancel()
	} else {
		mode = ModePath
		presence, capturedPath, isDir, exitCode, preExit, runErr = runPathMode(ctx, cfg, zw)
	}
	duration := now().Sub(start)

	zCloseErr := zw.Close()

	failErr := runErr
	if failErr == nil && exitCode != 0 {
		failErr = fmt.Errorf("hook exit %d", exitCode)
	}
	if failErr == nil {
		if cErr := ctx.Err(); cErr != nil {
			failErr = cErr
		}
	}
	if failErr == nil && zCloseErr != nil {
		failErr = fmt.Errorf("close zstd writer: %w", zCloseErr)
	}

	if failErr != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		recordFailure(dir, now(), cfg.Source, exitCode, failErr)
		// post_backup is a finalizer — if pre_backup left state behind
		// (stopped a daemon, held a lock), the operator still wants
		// post to run to undo it. The capture error stays the primary
		// failure; a non-zero post exit is logged alongside so it does
		// not mask the root cause. DEVM_CAPTURED_PATH is omitted
		// because nothing was tarred. The post context comes fresh
		// from the caller's ctx, bounded by PostTimeout: a cancelled
		// step context must not cancel post.
		if presence.PostBackup {
			runPostBackupAfterFailure(ctx, cfg, dir, now, preExit)
		}
		return Meta{}, failErr
	}

	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return Meta{}, fmt.Errorf("snapshot: fsync archive: %w", err)
	}
	info, err := tmp.Stat()
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return Meta{}, fmt.Errorf("snapshot: stat archive: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return Meta{}, fmt.Errorf("snapshot: close archive: %w", err)
	}

	finalPath := filepath.Join(dir, ts+".zst")
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return Meta{}, fmt.Errorf("snapshot: rename archive: %w", err)
	}

	m := Meta{
		TS:          ts,
		HookName:    "backup_" + cfg.EntryName,
		ArchiveName: cfg.EntryName,
		Source:      cfg.Source,
		ExitCode:    exitCode,
		DurationMs:  duration.Milliseconds(),
		SizeBytes:   info.Size(),
		Mode:        mode,
		Status:      StatusOk,
	}
	if mode == ModePath {
		m.CapturedPath = capturedPath
		m.IsDir = isDir
	}

	// Steps past the rename can still fail (bad permissions, disk
	// full, a Prune I/O error). A naked return here would leave a
	// half-finalised <ts>.zst — no sidecar, latest unmoved — and
	// Prune counts bare .zst files toward Keep, so the orphan could
	// evict a valid older archive on the next run. Treat each
	// post-rename failure like any other: scrub the archive and the
	// sidecar if present, log the failure, surface the error.
	metaPath := filepath.Join(dir, ts+".meta.json")
	rollback := func(step string, cause error) {
		_ = os.Remove(finalPath)
		_ = os.Remove(metaPath)
		recordFailure(dir, now(), cfg.Source, exitCode, fmt.Errorf("%s: %w", step, cause))
	}
	if err := WriteMeta(dir, ts, m); err != nil {
		rollback("write meta", err)
		return Meta{}, err
	}
	// SelectLatest, not ts, so post_backup can later flip Status to
	// post_failed and re-select back to the previous ok archive. On
	// the fresh-ok path this returns the same ts we just wrote.
	latestTS, err := SelectLatest(dir)
	if err != nil {
		rollback("select latest", err)
		return Meta{}, err
	}
	if latestTS != "" {
		if err := UpdateLatest(dir, latestTS); err != nil {
			rollback("update latest", err)
			return Meta{}, err
		}
	}
	if cfg.Keep > 0 {
		if _, err := Prune(dir, cfg.Keep); err != nil {
			rollback("prune", err)
			return Meta{}, err
		}
	}

	// Archive is committed to disk. post_backup runs last on the happy
	// path; its non-zero exit flips Meta.Status to post_failed but
	// keeps the archive — the operator's handle to the archive is
	// m.TS alongside a non-nil returned error. The post context is
	// fresh from the caller's ctx, bounded by PostTimeout, so a step
	// that burned through its deadline does not cancel post.
	if presence.PostBackup {
		postCtx, cancelPost := phaseCtx(ctx, cfg.PostTimeout)
		postCode, postErr := cfg.HookRunner.RunPostBackup(postCtx, cfg.EntryName, postBackupEnv(cfg.EntryName, capturedPath, preExit))
		cancelPost()
		if postErr != nil || postCode != 0 {
			var finalErr error
			if postErr != nil {
				finalErr = fmt.Errorf("post_backup_%s: %w", cfg.EntryName, postErr)
			} else {
				finalErr = fmt.Errorf("post_backup_%s exit %d", cfg.EntryName, postCode)
			}
			m.Status = StatusPostFailed
			if werr := WriteMeta(dir, ts, m); werr != nil {
				// A re-write failure here is rare (we just wrote this
				// sidecar) but recoverable: the archive is intact and
				// the returned error still surfaces the post failure.
				// Log it so the on-disk/returned Status mismatch is
				// visible.
				recordFailure(dir, now(), cfg.Source, postCode, fmt.Errorf("rewrite meta for post_failed: %w", werr))
			}
			// Re-aim latest at the newest ok archive now that this one
			// is flagged. SelectLatest falls back to the newest
			// post_failed when no ok exists, matching today's "point
			// at the newly-captured ts" when it is the only archive.
			if sel, serr := SelectLatest(dir); serr != nil {
				recordFailure(dir, now(), cfg.Source, postCode, fmt.Errorf("select latest after post_failed: %w", serr))
			} else if sel != "" {
				if uerr := UpdateLatest(dir, sel); uerr != nil {
					recordFailure(dir, now(), cfg.Source, postCode, fmt.Errorf("update latest after post_failed: %w", uerr))
				}
			}
			recordFailure(dir, now(), cfg.Source, postCode, finalErr)
			return m, finalErr
		}
	}
	return m, nil
}

// postBackupEnv builds the env map for RunPostBackup. DEVM_SNAPSHOT_NAME
// is always set. DEVM_CAPTURED_PATH is included only when capture
// actually produced a target (success path). DEVM_PRE_EXIT_CODE is
// included only when pre_backup_<name> ran, regardless of outcome.
// Absence vs. empty string matters here — a shell hook that checks
// `${DEVM_PRE_EXIT_CODE:-}` must be able to distinguish "pre didn't
// run" from "pre ran and returned 0".
func postBackupEnv(entry, capturedPath string, preExit *int) map[string]string {
	env := map[string]string{"DEVM_SNAPSHOT_NAME": entry}
	if capturedPath != "" {
		env["DEVM_CAPTURED_PATH"] = capturedPath
	}
	if preExit != nil {
		env["DEVM_PRE_EXIT_CODE"] = strconv.Itoa(*preExit)
	}
	return env
}

// runPostBackupAfterFailure calls post_backup on the capture-failed
// path. The capture error is the primary failure the caller returns;
// this helper only logs post's own outcome so it is visible to
// operators without masking the root cause. DEVM_CAPTURED_PATH is
// omitted because nothing was tarred. callerCtx is the un-cancelled
// context passed into Capture; this helper derives its own PostTimeout
// deadline from it so a cancelled step/pre context never pre-cancels
// the finalizer.
func runPostBackupAfterFailure(callerCtx context.Context, cfg CaptureConfig, dir string, now func() time.Time, preExit *int) {
	postCtx, cancel := phaseCtx(callerCtx, cfg.PostTimeout)
	defer cancel()
	postCode, postErr := cfg.HookRunner.RunPostBackup(postCtx, cfg.EntryName, postBackupEnv(cfg.EntryName, "", preExit))
	if postErr == nil && postCode == 0 {
		return
	}
	var reason error
	if postErr != nil {
		reason = fmt.Errorf("post_backup_%s: %v after capture failure", cfg.EntryName, postErr)
	} else {
		reason = fmt.Errorf("post_backup_%s exit %d after capture failure", cfg.EntryName, postCode)
	}
	recordFailure(dir, now(), cfg.Source, postCode, reason)
}

// phaseCtx returns a child of parent bounded by d. A zero or negative
// d disables the bound; the returned cancel is always safe to call. A
// per-phase deadline is always derived from the caller's parent ctx,
// never from a sibling phase's ctx, so one phase's timeout cannot
// cancel another's.
func phaseCtx(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, d)
}

// acquireLock waits for Lock(dir) but returns early on ctx
// cancellation. Lock itself blocks indefinitely, so we race it
// against ctx.Done() in a goroutine and hand the orphan the job of
// releasing the lock when it eventually arrives.
func acquireLock(ctx context.Context, dir string) (func(), error) {
	type result struct {
		unlock func()
		err    error
	}
	ch := make(chan result, 1)
	go func() {
		unlock, err := Lock(dir)
		ch <- result{unlock, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		return r.unlock, nil
	case <-ctx.Done():
		go func() {
			r := <-ch
			if r.err == nil {
				r.unlock()
			}
		}()
		return nil, ctx.Err()
	}
}

// runPathMode runs the path-mode capture pipeline: look up which
// lifecycle hooks exist, invoke pre_backup_<name> when present, apply
// its optional `file:///…` override to the capture target, stat the
// resolved target, and hand off to the TarRunner. Each phase carves
// its own context from callerCtx, bounded by the phase's timeout —
// one phase's deadline never cancels another's. Returns the hook
// presence bitmap (so Capture can decide whether to finalize with
// post_backup), the resolved absolute path (what Meta.CapturedPath
// records; empty if we never got to tar), whether it is a directory,
// the exit code of whichever sub-step last ran, a pointer to the
// pre_backup exit code (nil if pre was not called or exited through a
// transport error — set to the hook's own exit only when the hook
// actually ran cleanly through to a numeric code, so post sees
// DEVM_PRE_EXIT_CODE unset in the "pre did not report" case), and the
// terminal error if any.
func runPathMode(callerCtx context.Context, cfg CaptureConfig, w io.Writer) (presence HookPresence, capturedPath string, isDir bool, exitCode int, preExit *int, err error) {
	// HookExists is step preamble — share the step bound.
	stepCtxForExists, cancelExists := phaseCtx(callerCtx, cfg.StepTimeout)
	presence, err = cfg.HookRunner.HookExists(stepCtxForExists, cfg.EntryName)
	cancelExists()
	if err != nil {
		return presence, "", false, 0, nil, fmt.Errorf("snapshot %s: check hooks: %w", cfg.EntryName, err)
	}

	declared := *cfg.Path
	target := declared

	if presence.PreBackup {
		env := map[string]string{
			"DEVM_SNAPSHOT_NAME": cfg.EntryName,
			"DEVM_SNAPSHOT_PATH": declared,
		}
		preCtx, cancelPre := phaseCtx(callerCtx, cfg.PreTimeout)
		line, code, perr := cfg.HookRunner.RunPreBackup(preCtx, cfg.EntryName, env)
		cancelPre()
		// A transport error means the pre hook did not run cleanly to a
		// numeric exit, so DEVM_PRE_EXIT_CODE must stay unset in post's
		// env. Only a clean (perr == nil) invocation sets preExit —
		// including a non-zero exit code from the hook itself, which is
		// what the post finalizer uses to decide its own recovery.
		if perr != nil {
			return presence, "", false, code, nil, fmt.Errorf("pre_backup_%s: %w", cfg.EntryName, perr)
		}
		c := code
		preExit = &c
		if code != 0 {
			return presence, "", false, code, preExit, fmt.Errorf("pre_backup_%s: exit %d", cfg.EntryName, code)
		}
		override, parseErr := parsePreBackupOverride(cfg.EntryName, line)
		if parseErr != nil {
			return presence, "", false, 0, preExit, parseErr
		}
		if override != "" {
			target = override
		}
	}

	// Stat and tar share the step bound — StatPath is preparation for
	// the tar invocation that follows it.
	stepCtx, cancelStep := phaseCtx(callerCtx, cfg.StepTimeout)
	defer cancelStep()

	exists, isDir, resolved, statErr := cfg.TarRunner.StatPath(stepCtx, cfg.EntryName, target)
	if statErr != nil {
		return presence, "", false, 0, preExit, fmt.Errorf("snapshot %s: stat %s: %w", cfg.EntryName, target, statErr)
	}
	if !exists {
		return presence, "", false, 0, preExit, fmt.Errorf("snapshot %s: path %s does not exist", cfg.EntryName, target)
	}
	resolved = filepath.Clean(resolved)

	code, tarErr := cfg.TarRunner.RunBackupTar(stepCtx, cfg.EntryName, resolved, filepath.Base(declared), isDir, w)
	return presence, resolved, isDir, code, preExit, tarErr
}

// parsePreBackupOverride implements the strict pre_backup stdout
// protocol: empty is fine (no override); otherwise exactly one line
// matching ^file:///.+$ with no embedded newline (a single trailing
// newline is tolerated as the shell terminator). Anything else is a
// hook-authoring bug and is rejected loudly so the operator notices.
// Returns the resolved override path (after stripping `file://`) or
// an empty string when the line was empty.
func parsePreBackupOverride(entry, raw string) (string, error) {
	rejected := func() error {
		return fmt.Errorf("pre_backup_%s: stdout must be empty or a single file:/// line, got %q", entry, raw)
	}
	trimmed := strings.TrimSuffix(raw, "\n")
	if trimmed == "" {
		return "", nil
	}
	if strings.Contains(trimmed, "\n") {
		return "", rejected()
	}
	const prefix = "file:///"
	if !strings.HasPrefix(trimmed, prefix) || len(trimmed) <= len(prefix) {
		return "", rejected()
	}
	return strings.TrimPrefix(trimmed, "file://"), nil
}

// recordFailure appends one JSON line to failures.log. Errors
// writing the failure log are swallowed — we are already in the
// failure path and surfacing a second error would mask the first.
func recordFailure(dir string, at time.Time, source Source, exitCode int, cause error) {
	line, err := json.Marshal(struct {
		TS       string `json:"ts"`
		Source   Source `json:"source"`
		ExitCode int    `json:"exit_code"`
		Reason   string `json:"reason"`
	}{
		TS:       NewTimestamp(at),
		Source:   source,
		ExitCode: exitCode,
		Reason:   cause.Error(),
	})
	if err != nil {
		return
	}
	_ = AppendFailure(dir, string(line))
}
