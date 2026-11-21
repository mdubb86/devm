// Package snapshot holds the Mac-side storage primitives for devm's
// snapshot archives: filename conventions, the meta.json sidecar,
// the failures.log ring, the per-entry flock, latest-symlink
// maintenance, and prune.
//
// Each snapshotted entry (one per guest path devm captures — e.g. a
// Postgres volume) lives in its own directory alongside the volumes
// mirror:
//
//	<RuntimeDir>/<projectID>/snapshots/<entryName>/
//	    <ts>.zst            archive bytes
//	    <ts>.meta.json      sidecar
//	    latest -> <ts>.zst  symlink to the newest archive
//	    failures.log        append-only ring, newest at bottom
//	    .lock               flock target serializing captures/restores
//
// Primitives here are I/O-only and return errors up. Callers decide
// what to log.
package snapshot

import (
	"context"
	"io"
	"path/filepath"

	"github.com/mdubb86/devm/internal/identity"
)

// HookRunner runs a per-entry snapshot hook inside the guest. The
// daemon implements this against the guest-API socket; tests swap in
// an in-process fake. exitCode is the shell exit status of the hook
// (0 is success). err is a transport or execution failure (SSH drop,
// process could not be started, context cancelled before completion).
// Capture and Restore treat a non-zero exitCode or non-nil err as a
// failure.
//
// RunBackup streams the backup hook's stdout to the Mac-side writer.
// RunRestore streams the Mac-side reader into the restore hook's
// stdin.
//
// RunPreBackup / RunPreRestore / RunPostBackup / RunPostRestore run
// the matching lifecycle hook inside the guest and route its stderr
// to the daemon log line-prefixed as "snapshot <entry> <hookname>: ".
// RunPreBackup additionally captures the hook's first line of stdout
// (trimmed) as a protocol directive — path-mode uses this to receive
// a `file:///abs/path` from the guest. The other three discard stdout.
// env is exported into the hook's environment; callers assemble the
// map (DEVM_SNAPSHOT_*, DEVM_PRE_EXIT_CODE, DEVM_CAPTURED_PATH, ...).
//
// HookExists introspects the sourced /opt/devm/devm.sh for the per-
// entry hook functions in one shell-out and returns a presence bitmap.
type HookRunner interface {
	RunBackup(ctx context.Context, entryName string, stdout io.Writer) (exitCode int, err error)
	RunRestore(ctx context.Context, entryName string, stdin io.Reader) (exitCode int, err error)
	RunPreBackup(ctx context.Context, entryName string, env map[string]string) (stdoutLine string, exitCode int, err error)
	RunPostBackup(ctx context.Context, entryName string, env map[string]string) (exitCode int, err error)
	RunPreRestore(ctx context.Context, entryName string, env map[string]string) (exitCode int, err error)
	RunPostRestore(ctx context.Context, entryName string, env map[string]string) (exitCode int, err error)
	HookExists(ctx context.Context, entryName string) (HookPresence, error)
}

// HookPresence records which per-entry hook functions are defined in
// the guest's sourced /opt/devm/devm.sh. Populated by one call to
// HookExists so callers avoid four separate shell-outs when deciding
// which pre/post hooks to invoke.
type HookPresence struct {
	Backup      bool
	Restore     bool
	PreBackup   bool
	PostBackup  bool
	PreRestore  bool
	PostRestore bool
}

// TarRunner executes tar+zstd inside the guest for path-mode snapshots.
// The daemon implements this against the guest-API socket; tests swap
// in an in-process fake.
//
// RunBackupTar streams `tar -C <parent> -cf - <basename>` (or
// `tar -C <parent> -cf - <file>` for a single file) through to the
// Mac-side writer. source is the resolved absolute path devm captures;
// declaredBasename is filepath.Base of the originally declared
// `path:` and is the name the restore side re-materializes under when
// the operator later restores to the same parent. isDir chooses
// between the directory and file tar branches.
//
// RunRestoreTar is the mirror: it reads the Mac-side archive stream
// and extracts into the guest. declaredPath is the fully-qualified
// guest path the operator asked to restore to (overrideable at the
// Mac CLI). exitCode is the tar process exit status (0 is success);
// err is a transport or execution failure.
//
// StatPath queries the guest for the path's existence, type, and
// symlink-resolved absolute location. Returns exists=false with a
// nil err when the path is absent. A non-nil err signals a
// transport or shell failure, not path absence.
type TarRunner interface {
	RunBackupTar(ctx context.Context, entryName, source, declaredBasename string, isDir bool, stdout io.Writer) (exitCode int, err error)
	RunRestoreTar(ctx context.Context, entryName, declaredPath string, isDir bool, stdin io.Reader) (exitCode int, err error)
	StatPath(ctx context.Context, entryName, guestPath string) (exists bool, isDir bool, resolvedPath string, err error)
}

// Source names what triggered a snapshot run.
type Source string

const (
	SourceCLI        Source = "cli"
	SourceOnStop     Source = "on_stop"
	SourceOnTeardown Source = "on_teardown"
	SourceCron       Source = "cron"
	// SourceUnknown is what ReadMeta returns when the sidecar is
	// missing but the archive is present — the archive's lineage was
	// lost but the bytes are still restorable.
	SourceUnknown Source = "unknown"
)

// Mode names how an archive's bytes were produced. "stream" is a
// hook-driven tar/stream contract (backup_<name> writes to stdout,
// restore_<name> reads from stdin). "path" is devm capturing a
// declared guest path directly — no hooks required.
type Mode string

const (
	ModeStream Mode = "stream"
	ModePath   Mode = "path"
)

// Status marks how a snapshot run concluded. StatusOk means capture
// finished cleanly. StatusPostFailed means the archive bytes are
// good but a path-mode post-hook returned non-zero afterwards — the
// archive is still restorable, but something downstream did not
// recover and operators may want to investigate.
type Status string

const (
	StatusOk         Status = "ok"
	StatusPostFailed Status = "post_failed"
)

// Meta is the sidecar describing one archive. Serialized next to the
// archive as <ts>.meta.json so operators and downstream tools can
// read snapshot lineage without the daemon.
type Meta struct {
	TS          string `json:"ts"`
	HookName    string `json:"hook_name"`
	ArchiveName string `json:"archive_name"`
	Source      Source `json:"source"`
	ExitCode    int    `json:"exit_code"`
	DurationMs  int64  `json:"duration_ms"`
	// SizeBytes is the on-disk size of the compressed <ts>.zst archive,
	// not the uncompressed hook payload. 0 when the hook wrote nothing.
	SizeBytes    int64  `json:"size_bytes"`
	Mode         Mode   `json:"mode,omitempty"`
	CapturedPath string `json:"captured_path,omitempty"`
	// IsDir records whether the captured guest path was a directory
	// (true) or a single file (false). Restore uses it to pick tar
	// extract flags. Set at capture time; absent on stream-mode and
	// on v0.31 archives.
	IsDir  bool   `json:"is_dir,omitempty"`
	Status Status `json:"status,omitempty"`
	// Orphan marks a list-response row whose archive directory exists
	// on disk but whose entry name is absent from the current
	// devm.yaml's snapshots: order. Never written to the sidecar; set
	// only on synthetic rows the list handler builds for operators who
	// renamed or removed a snapshot entry. The `omitempty` keeps the
	// field invisible on normal archive metas.
	Orphan bool `json:"orphan,omitempty"`
}

// EffectiveMode returns the archive's mode, defaulting to ModeStream
// when Mode is empty. v0.31 sidecars have no mode field; this is the
// single resolver downstream code uses to answer "what mode is this
// archive".
func (m Meta) EffectiveMode() Mode {
	if m.Mode == "" {
		return ModeStream
	}
	return m.Mode
}

// EntryDir returns the per-entry snapshot directory, sibling to the
// project's volumes mirror. The path matches the volumes mirror
// layout — <RuntimeDir>/<projectID>/... — with no interposed
// "projects/" segment.
func EntryDir(ident identity.Config, projectID, entryName string) string {
	return filepath.Join(ident.RuntimeDir(), projectID, "snapshots", entryName)
}
