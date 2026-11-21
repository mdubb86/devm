package snapshot

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/schema"
)

// captureAndRestoreRunner is a HookRunner for restore tests. RunBackup
// writes a fixed payload so a round-trip Capture → Restore has real
// bytes to compare. RunRestore reads the entire stream and stores it
// so the test can assert on what the hook saw.
type captureAndRestoreRunner struct {
	backupPayload []byte

	// restoreDelay holds RunRestore busy so the concurrency test can
	// prove the flock blocks the second caller.
	restoreDelay time.Duration

	// backupDelay holds RunBackup busy so a concurrent Restore blocks
	// on the flock.
	backupDelay time.Duration

	active    *atomic.Int32
	maxActive *atomic.Int32

	mu       sync.Mutex
	received []byte
	calls    int
}

func (r *captureAndRestoreRunner) RunBackup(ctx context.Context, entryName string, stdout io.Writer) (int, error) {
	if r.active != nil {
		cur := r.active.Add(1)
		for {
			m := r.maxActive.Load()
			if cur <= m || r.maxActive.CompareAndSwap(m, cur) {
				break
			}
		}
		defer r.active.Add(-1)
	}
	if r.backupDelay > 0 {
		select {
		case <-time.After(r.backupDelay):
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	if len(r.backupPayload) > 0 {
		if _, err := stdout.Write(r.backupPayload); err != nil {
			return 0, err
		}
	}
	return 0, nil
}

// Pre/post lifecycle hooks and HookExists are unused by stream-mode
// capture/restore tests; panic rather than silently succeed so a
// future change can't route through this stub unnoticed.
func (r *captureAndRestoreRunner) RunPreBackup(ctx context.Context, entryName string, env map[string]string) (string, int, error) {
	panic("captureAndRestoreRunner.RunPreBackup: not used in these tests")
}
func (r *captureAndRestoreRunner) RunPostBackup(ctx context.Context, entryName string, env map[string]string) (int, error) {
	panic("captureAndRestoreRunner.RunPostBackup: not used in these tests")
}
func (r *captureAndRestoreRunner) RunPreRestore(ctx context.Context, entryName string, env map[string]string) (int, error) {
	panic("captureAndRestoreRunner.RunPreRestore: not used in these tests")
}
func (r *captureAndRestoreRunner) RunPostRestore(ctx context.Context, entryName string, env map[string]string) (int, error) {
	panic("captureAndRestoreRunner.RunPostRestore: not used in these tests")
}
func (r *captureAndRestoreRunner) HookExists(ctx context.Context, entryName string) (HookPresence, error) {
	panic("captureAndRestoreRunner.HookExists: not used in these tests")
}

func (r *captureAndRestoreRunner) RunRestore(ctx context.Context, entryName string, stdin io.Reader) (int, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	if r.active != nil {
		cur := r.active.Add(1)
		for {
			m := r.maxActive.Load()
			if cur <= m || r.maxActive.CompareAndSwap(m, cur) {
				break
			}
		}
		defer r.active.Add(-1)
	}
	if r.restoreDelay > 0 {
		select {
		case <-time.After(r.restoreDelay):
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	b, err := io.ReadAll(stdin)
	if err != nil {
		return 0, err
	}
	r.mu.Lock()
	r.received = b
	r.mu.Unlock()
	return 0, nil
}

// restoreFailRunner reports a non-zero exit on RunRestore so the test
// can prove exitCode != 0 surfaces as an error.
type restoreFailRunner struct {
	exit int
}

func (r *restoreFailRunner) RunBackup(ctx context.Context, entryName string, stdout io.Writer) (int, error) {
	return 0, nil
}

func (r *restoreFailRunner) RunRestore(ctx context.Context, entryName string, stdin io.Reader) (int, error) {
	// Drain stdin so Capture's writer side doesn't block (Capture
	// isn't involved here, but a real hook would still read).
	_, _ = io.Copy(io.Discard, stdin)
	return r.exit, nil
}

// restoreFailRunner is a stream-mode HookRunner; path-mode methods
// panic to flag any accidental routing through this stub.
func (r *restoreFailRunner) RunPreBackup(ctx context.Context, entryName string, env map[string]string) (string, int, error) {
	panic("restoreFailRunner.RunPreBackup: not used in these tests")
}
func (r *restoreFailRunner) RunPostBackup(ctx context.Context, entryName string, env map[string]string) (int, error) {
	panic("restoreFailRunner.RunPostBackup: not used in these tests")
}
func (r *restoreFailRunner) RunPreRestore(ctx context.Context, entryName string, env map[string]string) (int, error) {
	panic("restoreFailRunner.RunPreRestore: not used in these tests")
}
func (r *restoreFailRunner) RunPostRestore(ctx context.Context, entryName string, env map[string]string) (int, error) {
	panic("restoreFailRunner.RunPostRestore: not used in these tests")
}
func (r *restoreFailRunner) HookExists(ctx context.Context, entryName string) (HookPresence, error) {
	panic("restoreFailRunner.HookExists: not used in these tests")
}

// entriesWith builds a schema.OrderedMap[schema.Snapshot] that
// declares the given entry names with zero-value Snapshot values.
func entriesWith(names ...string) schema.OrderedMap[schema.Snapshot] {
	var m schema.OrderedMap[schema.Snapshot]
	for _, n := range names {
		m.Set(n, schema.Snapshot{})
	}
	return m
}

func TestRestore_RoundTripAgainstCapture(t *testing.T) {
	ident := newTestIdentity(t)
	payload := []byte("hello world from restore round-trip")
	runner := &captureAndRestoreRunner{backupPayload: payload}

	_, err := Capture(context.Background(), CaptureConfig{
		Ident:      ident,
		ProjectID:  "proj",
		EntryName:  "pg-data",
		Source:     SourceCLI,
		HookRunner: runner,
		Now:        stepClock(),
	})
	require.NoError(t, err)

	restored, err := Restore(context.Background(), RestoreConfig{
		Ident:      ident,
		ProjectID:  "proj",
		EntryName:  "pg-data",
		HookRunner: runner,
		Entries:    entriesWith("pg-data"),
	})
	require.NoError(t, err)
	assert.True(t, restored, "restore must report true when an archive was replayed")

	assert.Equal(t, 1, runner.calls, "RunRestore must be called exactly once")
	assert.True(t, bytes.Equal(payload, runner.received),
		"hook's stdin bytes must match the captured payload: got %q want %q",
		string(runner.received), string(payload))
}

func TestRestore_LatestSymlinkDefault(t *testing.T) {
	ident := newTestIdentity(t)
	clock := stepClock()
	runner := &captureAndRestoreRunner{backupPayload: []byte("first")}

	_, err := Capture(context.Background(), CaptureConfig{
		Ident:      ident,
		ProjectID:  "proj",
		EntryName:  "pg-data",
		Source:     SourceCLI,
		HookRunner: runner,
		Now:        clock,
	})
	require.NoError(t, err)

	runner2 := &captureAndRestoreRunner{backupPayload: []byte("second")}
	second, err := Capture(context.Background(), CaptureConfig{
		Ident:      ident,
		ProjectID:  "proj",
		EntryName:  "pg-data",
		Source:     SourceCLI,
		HookRunner: runner2,
		Now:        clock,
	})
	require.NoError(t, err)

	dir := EntryDir(ident, "proj", "pg-data")
	latest, err := LatestTS(dir)
	require.NoError(t, err)
	require.Equal(t, second.TS, latest)

	// No TS given → restore from latest = "second".
	restored, err := Restore(context.Background(), RestoreConfig{
		Ident:      ident,
		ProjectID:  "proj",
		EntryName:  "pg-data",
		HookRunner: runner2,
		Entries:    entriesWith("pg-data"),
	})
	require.NoError(t, err)
	assert.True(t, restored)
	assert.Equal(t, []byte("second"), runner2.received)
}

func TestRestore_ExplicitTSWins(t *testing.T) {
	ident := newTestIdentity(t)
	clock := stepClock()

	runner1 := &captureAndRestoreRunner{backupPayload: []byte("older")}
	older, err := Capture(context.Background(), CaptureConfig{
		Ident:      ident,
		ProjectID:  "proj",
		EntryName:  "pg-data",
		Source:     SourceCLI,
		HookRunner: runner1,
		Now:        clock,
	})
	require.NoError(t, err)

	runner2 := &captureAndRestoreRunner{backupPayload: []byte("newer")}
	newer, err := Capture(context.Background(), CaptureConfig{
		Ident:      ident,
		ProjectID:  "proj",
		EntryName:  "pg-data",
		Source:     SourceCLI,
		HookRunner: runner2,
		Now:        clock,
	})
	require.NoError(t, err)
	require.NotEqual(t, older.TS, newer.TS)

	restoreRunner := &captureAndRestoreRunner{}
	restored, err := Restore(context.Background(), RestoreConfig{
		Ident:      ident,
		ProjectID:  "proj",
		EntryName:  "pg-data",
		TS:         older.TS,
		HookRunner: restoreRunner,
		Entries:    entriesWith("pg-data"),
	})
	require.NoError(t, err)
	assert.True(t, restored)
	assert.Equal(t, []byte("older"), restoreRunner.received,
		"explicit older TS must win over latest")
}

// TestRestore_MissingLatestIsNoop covers the first-boot case install
// hooks rely on: there's no archive yet, so Restore must report that
// nothing happened without surfacing a failure.
func TestRestore_MissingLatestIsNoop(t *testing.T) {
	ident := newTestIdentity(t)
	runner := &captureAndRestoreRunner{}

	restored, err := Restore(context.Background(), RestoreConfig{
		Ident:      ident,
		ProjectID:  "proj",
		EntryName:  "pg-data",
		HookRunner: runner,
		Entries:    entriesWith("pg-data"),
	})
	require.NoError(t, err, "no archive is a normal first-boot case, not a failure")
	assert.False(t, restored, "restored must be false when there was nothing to replay")
	assert.Equal(t, 0, runner.calls, "hook must not be called when no archive exists")
}

// TestRestore_MissingExplicitTSErrors pins the other half of the
// contract: when the caller named a specific TS and that archive is
// absent, it IS a failure — the caller asked for a thing that does
// not exist.
func TestRestore_MissingExplicitTSErrors(t *testing.T) {
	ident := newTestIdentity(t)
	runner := &captureAndRestoreRunner{}

	restored, err := Restore(context.Background(), RestoreConfig{
		Ident:      ident,
		ProjectID:  "proj",
		EntryName:  "pg-data",
		TS:         "2026-03-01T1200-00",
		HookRunner: runner,
		Entries:    entriesWith("pg-data"),
	})
	require.Error(t, err, "an explicit TS the sidecar does not describe must surface as a failure")
	assert.False(t, restored)
	assert.Equal(t, 0, runner.calls)
}

func TestRestore_ArchiveWithoutEntryErrors(t *testing.T) {
	ident := newTestIdentity(t)
	dir := EntryDir(ident, "proj", "pg-data")

	// Pre-plant an archive directory and a fake archive so the error
	// has something real to describe. Restore must bail before reading
	// any of it.
	require.NoError(t, os.MkdirAll(dir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "2026-03-01T1200-00.zst"), []byte("not a real archive"), 0644))

	runner := &captureAndRestoreRunner{}
	restored, err := Restore(context.Background(), RestoreConfig{
		Ident:      ident,
		ProjectID:  "proj",
		EntryName:  "pg-data",
		HookRunner: runner,
		// Entries declares some OTHER entry, not pg-data.
		Entries: entriesWith("other-entry"),
	})
	require.Error(t, err)
	assert.False(t, restored)
	assert.Contains(t, err.Error(), dir,
		"error must name the on-disk path so the operator can inspect")
	assert.Equal(t, 0, runner.calls, "hook must not be called for an orphan entry")
}

func TestRestore_SerialisesWithCapture(t *testing.T) {
	ident := newTestIdentity(t)
	dir := EntryDir(ident, "proj", "pg-data")
	clock := stepClock()

	var active, maxActive atomic.Int32

	// First capture lands an archive we can restore from.
	seed := &captureAndRestoreRunner{backupPayload: []byte("seed")}
	_, err := Capture(context.Background(), CaptureConfig{
		Ident:      ident,
		ProjectID:  "proj",
		EntryName:  "pg-data",
		Source:     SourceCLI,
		HookRunner: seed,
		Now:        clock,
	})
	require.NoError(t, err)

	// Slow capture holds the flock; concurrent restore must block.
	slowCapture := &captureAndRestoreRunner{
		backupPayload: []byte("slow"),
		backupDelay:   60 * time.Millisecond,
		active:        &active,
		maxActive:     &maxActive,
	}
	restoreRunner := &captureAndRestoreRunner{
		active:    &active,
		maxActive: &maxActive,
	}

	var wg sync.WaitGroup
	wg.Add(2)
	captureErr := make(chan error, 1)
	restoreErr := make(chan error, 1)
	go func() {
		defer wg.Done()
		_, err := Capture(context.Background(), CaptureConfig{
			Ident:      ident,
			ProjectID:  "proj",
			EntryName:  "pg-data",
			Source:     SourceCLI,
			HookRunner: slowCapture,
			Now:        clock,
		})
		captureErr <- err
	}()
	// Nudge the capture goroutine to run first so it holds the lock
	// when the restore attempt arrives.
	time.Sleep(10 * time.Millisecond)
	go func() {
		defer wg.Done()
		_, err := Restore(context.Background(), RestoreConfig{
			Ident:      ident,
			ProjectID:  "proj",
			EntryName:  "pg-data",
			HookRunner: restoreRunner,
			Entries:    entriesWith("pg-data"),
		})
		restoreErr <- err
	}()
	wg.Wait()
	close(captureErr)
	close(restoreErr)

	require.NoError(t, <-captureErr)
	require.NoError(t, <-restoreErr)

	assert.Equal(t, int32(1), maxActive.Load(),
		"flock must serialize capture and restore — never both hook bodies at once")

	// Restore snapshots `latest` at request time (pre-lock) so the
	// drift check can refuse a doomed call without first blocking on
	// the flock. The slow capture that landed while restore was
	// waiting for the lock produced a newer archive, but restore
	// still opens the seed archive it resolved at request time.
	assert.Equal(t, []byte("seed"), restoreRunner.received,
		"restore must have opened the archive it resolved pre-lock, not the newer one that landed during the wait")

	_, err = os.Stat(filepath.Join(dir, "latest"))
	require.NoError(t, err)
}

// metaOverrides is the drift-test knob set: the fields the helper
// rewrites in the archive's meta sidecar so a test can simulate any
// mode/captured-path/status combination without actually running
// path-mode capture.
type metaOverrides struct {
	Mode         Mode
	CapturedPath string
	Status       Status
	IsDir        bool
}

// restoreSetup is the handle a drift test holds onto between
// makeRestoreSetup() and run(): the already-captured archive's
// identity/name, the config the test will mutate (Force, TS, ...),
// and the yaml entry whose Path the test flips to drive drift.
type restoreSetup struct {
	ident     identity.Config
	projectID string
	name      string
	cfg       RestoreConfig
	entry     schema.Snapshot
}

// run stitches the (test-mutated) entry back into cfg.Entries at
// call time and invokes Restore. Writing to setup.entry after setup
// has to land somewhere — OrderedMap holds values, not pointers —
// so the helper does the final Set here instead of asking tests to.
// The bool Restore reports is dropped: drift tests check only the
// error path.
func (s *restoreSetup) run() error {
	var m schema.OrderedMap[schema.Snapshot]
	m.Set(s.name, s.entry)
	s.cfg.Entries = m
	_, err := Restore(context.Background(), s.cfg)
	return err
}

// makeRestoreSetup seeds a stream-mode archive for "pg-data" and
// rewrites its meta sidecar with overrides so the drift tests can
// fake any (Mode, CapturedPath) combination. The returned setup's
// cfg uses a fresh captureAndRestoreRunner — the drift-passing cases
// then go on to call RunRestore against a real zstd stream.
func makeRestoreSetup(t *testing.T, overrides metaOverrides) *restoreSetup {
	t.Helper()
	ident := newTestIdentity(t)
	name := "pg-data"
	projectID := "proj"

	seed := &captureAndRestoreRunner{backupPayload: []byte("seed")}
	meta, err := Capture(context.Background(), CaptureConfig{
		Ident:      ident,
		ProjectID:  projectID,
		EntryName:  name,
		Source:     SourceCLI,
		HookRunner: seed,
		Now:        stepClock(),
	})
	require.NoError(t, err)

	meta.Mode = overrides.Mode
	meta.CapturedPath = overrides.CapturedPath
	if overrides.Status != "" {
		meta.Status = overrides.Status
	}
	meta.IsDir = overrides.IsDir
	dir := EntryDir(ident, projectID, name)
	require.NoError(t, WriteMeta(dir, meta.TS, meta))

	return &restoreSetup{
		ident:     ident,
		projectID: projectID,
		name:      name,
		cfg: RestoreConfig{
			Ident:      ident,
			ProjectID:  projectID,
			EntryName:  name,
			HookRunner: &captureAndRestoreRunner{},
		},
	}
}

func TestRestore_ModeDriftRefused(t *testing.T) {
	setup := makeRestoreSetup(t, metaOverrides{Mode: ModeStream})
	setup.entry.Path = ptr("/foo")
	err := setup.run()
	require.Error(t, err)
	require.ErrorContains(t, err, "archive is stream-mode")
	require.ErrorContains(t, err, "yaml now declares path")
	require.ErrorContains(t, err, "--force")
	require.ErrorContains(t, err, "devm snapshot clear pg-data")
}

func TestRestore_PathDriftRefused(t *testing.T) {
	setup := makeRestoreSetup(t, metaOverrides{Mode: ModePath, CapturedPath: "/old"})
	setup.entry.Path = ptr("/new")
	err := setup.run()
	require.Error(t, err)
	require.ErrorContains(t, err, "captured from /old")
	require.ErrorContains(t, err, "yaml now says /new")
	require.ErrorContains(t, err, "--force")
}

func TestRestore_ForceBypassesDrift(t *testing.T) {
	setup := makeRestoreSetup(t, metaOverrides{Mode: ModeStream})
	setup.entry.Path = ptr("/foo")
	setup.cfg.Force = true
	// Force bypasses drift and the subsequent stream-mode RunRestore
	// against a real zstd archive succeeds in the fake.
	err := setup.run()
	require.NoError(t, err)
}

func TestRestore_V031EmptyMetaMode_TreatedAsStream_NoDriftWhenYamlStream(t *testing.T) {
	// v0.31 sidecars have no mode field; EffectiveMode() resolves to
	// ModeStream. Yaml here has Path == nil (stream), so no drift.
	setup := makeRestoreSetup(t, metaOverrides{Mode: ""})
	setup.entry.Path = nil
	err := setup.run()
	require.NoError(t, err)
}

// TestRestore_StreamMode_RefusesPathArchive pins the stream-side of
// the mode-drift invariant: a path-mode archive must not be replayed
// into a stream-mode yaml entry, independent of the mode-drift test
// above. If the drift check is ever narrowed to fire only when yaml is
// path-mode, this test fails.
func TestRestore_StreamMode_RefusesPathArchive(t *testing.T) {
	setup := makeRestoreSetup(t, metaOverrides{Mode: ModePath, CapturedPath: "/foo"})
	setup.entry.Path = nil // yaml says stream
	err := setup.run()
	require.Error(t, err)
	require.ErrorContains(t, err, "archive is path-mode")
	require.ErrorContains(t, err, "yaml now declares stream")
}

// TestRestore_StreamMode_AcceptsStreamArchive pins the happy stream
// path: stream-mode yaml + stream-mode archive restores cleanly
// through the bytes-in-bytes-out hook contract.
func TestRestore_StreamMode_AcceptsStreamArchive(t *testing.T) {
	setup := makeRestoreSetup(t, metaOverrides{Mode: ModeStream})
	setup.entry.Path = nil
	require.NoError(t, setup.run())
}

// --- Path-mode restore test harness ---

// pathRestoreFakeRunner is a HookRunner for path-mode restore tests.
// presence drives HookExists so individual tests opt pre/post hooks
// in. Backup and stream RunRestore panic so a future change can't
// silently route through this stub.
type pathRestoreFakeRunner struct {
	presence    HookPresence
	preRestore  func(name string, env map[string]string) (int, error)
	postRestore func(name string, env map[string]string) (int, error)
}

func (r *pathRestoreFakeRunner) RunBackup(ctx context.Context, entryName string, stdout io.Writer) (int, error) {
	panic("pathRestoreFakeRunner.RunBackup: not used in path-mode restore tests")
}
func (r *pathRestoreFakeRunner) RunRestore(ctx context.Context, entryName string, stdin io.Reader) (int, error) {
	panic("pathRestoreFakeRunner.RunRestore: path-mode must not call the stream restore hook")
}
func (r *pathRestoreFakeRunner) RunPreBackup(ctx context.Context, entryName string, env map[string]string) (string, int, error) {
	panic("pathRestoreFakeRunner.RunPreBackup: not used in path-mode restore tests")
}
func (r *pathRestoreFakeRunner) RunPostBackup(ctx context.Context, entryName string, env map[string]string) (int, error) {
	panic("pathRestoreFakeRunner.RunPostBackup: not used in path-mode restore tests")
}
func (r *pathRestoreFakeRunner) RunPreRestore(ctx context.Context, entryName string, env map[string]string) (int, error) {
	if r.preRestore == nil {
		return 0, nil
	}
	return r.preRestore(entryName, env)
}
func (r *pathRestoreFakeRunner) RunPostRestore(ctx context.Context, entryName string, env map[string]string) (int, error) {
	if r.postRestore == nil {
		return 0, nil
	}
	return r.postRestore(entryName, env)
}
func (r *pathRestoreFakeRunner) HookExists(ctx context.Context, entryName string) (HookPresence, error) {
	return r.presence, nil
}

// tarRunnerStub is a TarRunner for path-mode restore tests. restoreTar
// nil returns (0, nil) so the setup's default call succeeds without
// configuration. RunBackupTar panics because restore tests never
// exercise the capture side.
type tarRunnerStub struct {
	restoreTar func(entry, path string, isDir bool, r io.Reader) (int, error)
}

func (t tarRunnerStub) RunBackupTar(ctx context.Context, entryName, source, declaredBasename string, isDir bool, stdout io.Writer) (int, error) {
	panic("tarRunnerStub.RunBackupTar: not used in restore tests")
}

func (t tarRunnerStub) RunRestoreTar(ctx context.Context, entryName, declaredPath string, isDir bool, stdin io.Reader) (int, error) {
	if t.restoreTar == nil {
		return 0, nil
	}
	return t.restoreTar(entryName, declaredPath, isDir, stdin)
}

func (t tarRunnerStub) StatPath(ctx context.Context, entryName, guestPath string) (bool, bool, string, error) {
	panic("tarRunnerStub.StatPath: not used in restore tests")
}

// pathRestoreSetup is the handle a path-mode restore test holds onto
// between makePathModeRestoreSetup and run(): the identity / cfg the
// test mutates (TarRunner, Force, ...), the yaml entry whose Path the
// test flips, and the hook-presence / callback knobs run() stitches
// into the runner at call time.
type pathRestoreSetup struct {
	ident       identity.Config
	projectID   string
	name        string
	entry       schema.Snapshot
	cfg         RestoreConfig
	runner      *pathRestoreFakeRunner
	hookExists  map[string]bool
	preRestore  func(name string, env map[string]string) (int, error)
	postRestore func(name string, env map[string]string) (int, error)
}

// presence rebuilds the HookPresence bitmap from hookExists keys, the
// same shape `declare -F` would surface. Keys use the fully-qualified
// function name ("pre_restore_<entry>") so tests declare them the way
// an operator would.
func (s *pathRestoreSetup) presence() HookPresence {
	e := s.name
	return HookPresence{
		Backup:      s.hookExists["backup_"+e],
		Restore:     s.hookExists["restore_"+e],
		PreBackup:   s.hookExists["pre_backup_"+e],
		PostBackup:  s.hookExists["post_backup_"+e],
		PreRestore:  s.hookExists["pre_restore_"+e],
		PostRestore: s.hookExists["post_restore_"+e],
	}
}

// run propagates the test-set hookExists / callbacks into the runner
// and stitches the current entry into cfg.Entries and cfg.Path before
// invoking Restore. OrderedMap holds values (not pointers), so this
// has to happen at call time — not at setup — for a test mutation of
// setup.entry to take effect.
func (s *pathRestoreSetup) run() error {
	s.runner.presence = s.presence()
	s.runner.preRestore = s.preRestore
	s.runner.postRestore = s.postRestore

	var m schema.OrderedMap[schema.Snapshot]
	m.Set(s.name, s.entry)
	s.cfg.Entries = m
	s.cfg.Path = s.entry.Path
	// Path-mode restore tests here check only the error surface. The
	// bool Restore reports is dropped.
	_, err := Restore(context.Background(), s.cfg)
	return err
}

// makePathModeRestoreSetup seeds a path-mode archive for "foo",
// aligns entry.Path with meta.CapturedPath so the drift check passes
// by default, and wires a TarRunner stub that succeeds silently. The
// archive bytes come from a stream-mode capture (seed payload); the
// test then rewrites the sidecar to mark the archive as path mode
// with the desired overrides.
func makePathModeRestoreSetup(t *testing.T, overrides metaOverrides) *pathRestoreSetup {
	t.Helper()
	ident := newTestIdentity(t)
	name := "foo"
	projectID := "proj"

	seed := &captureAndRestoreRunner{backupPayload: []byte("seed")}
	meta, err := Capture(context.Background(), CaptureConfig{
		Ident:      ident,
		ProjectID:  projectID,
		EntryName:  name,
		Source:     SourceCLI,
		HookRunner: seed,
		Now:        stepClock(),
	})
	require.NoError(t, err)

	captured := overrides.CapturedPath
	if captured == "" {
		captured = "/data/foo"
	}
	meta.Mode = overrides.Mode
	if meta.Mode == "" {
		meta.Mode = ModePath
	}
	meta.CapturedPath = captured
	meta.IsDir = overrides.IsDir
	if overrides.Status != "" {
		meta.Status = overrides.Status
	}
	dir := EntryDir(ident, projectID, name)
	require.NoError(t, WriteMeta(dir, meta.TS, meta))

	runner := &pathRestoreFakeRunner{}
	path := captured
	return &pathRestoreSetup{
		ident:     ident,
		projectID: projectID,
		name:      name,
		entry:     schema.Snapshot{Path: &path},
		runner:    runner,
		cfg: RestoreConfig{
			Ident:      ident,
			ProjectID:  projectID,
			EntryName:  name,
			HookRunner: runner,
			TarRunner:  tarRunnerStub{},
		},
	}
}

func TestRestore_PathMode_ExtractsToDeclaredPath(t *testing.T) {
	setup := makePathModeRestoreSetup(t, metaOverrides{Mode: ModePath, CapturedPath: "/target/thing"})
	var extractDest string
	setup.cfg.TarRunner = tarRunnerStub{
		restoreTar: func(entry, path string, isDir bool, r io.Reader) (int, error) {
			extractDest = path
			return 0, nil
		},
	}
	require.NoError(t, setup.run())
	assert.Equal(t, "/target/thing", extractDest)
}

// TestRestore_PathMode_TarRunnerSeesDeclaredPath is the mkdir-parent
// test adapted from the brief. The actual `mkdir -p "$(dirname …)"`
// is baked into the in-guest shell wrapper around tar (Task 11), not
// into this Mac-side flow — the path is a guest path, so an
// os.MkdirAll on the Mac would have no effect. What this test can
// verify is that the declared path threads through to TarRunner
// unchanged, so the guest-side mkdir+extract targets the right place.
func TestRestore_PathMode_TarRunnerSeesDeclaredPath(t *testing.T) {
	freshParent := filepath.Join(t.TempDir(), "newparent")
	target := filepath.Join(freshParent, "thing")
	setup := makePathModeRestoreSetup(t, metaOverrides{Mode: ModePath, CapturedPath: target})
	var gotPath string
	setup.cfg.TarRunner = tarRunnerStub{
		restoreTar: func(entry, path string, isDir bool, r io.Reader) (int, error) {
			gotPath = path
			return 0, nil
		},
	}
	require.NoError(t, setup.run())
	assert.Equal(t, target, gotPath)
}

func TestRestore_PathMode_PreRestoreFails_SkipsExtract_PostStillRuns(t *testing.T) {
	var extractCalled, postCalled bool
	setup := makePathModeRestoreSetup(t, metaOverrides{Mode: ModePath})
	setup.hookExists = map[string]bool{"pre_restore_foo": true, "post_restore_foo": true}
	setup.preRestore = func(name string, env map[string]string) (int, error) { return 2, nil }
	setup.cfg.TarRunner = tarRunnerStub{
		restoreTar: func(entry, path string, isDir bool, r io.Reader) (int, error) {
			extractCalled = true
			return 0, nil
		},
	}
	setup.postRestore = func(name string, env map[string]string) (int, error) {
		postCalled = true
		assert.Equal(t, "2", env["DEVM_PRE_EXIT_CODE"])
		return 0, nil
	}
	err := setup.run()
	require.Error(t, err)
	assert.False(t, extractCalled, "extract must not run after pre_restore fails")
	assert.True(t, postCalled, "post_restore is a finalizer — must run after pre fails")
}

func TestRestore_PathMode_PostRestoreGetsRestoredPathEnv(t *testing.T) {
	var gotEnv map[string]string
	setup := makePathModeRestoreSetup(t, metaOverrides{Mode: ModePath, CapturedPath: "/home/devm/.claude"})
	setup.hookExists = map[string]bool{"post_restore_foo": true}
	setup.postRestore = func(name string, env map[string]string) (int, error) {
		gotEnv = env
		return 0, nil
	}
	require.NoError(t, setup.run())
	assert.Equal(t, "/home/devm/.claude", gotEnv["DEVM_RESTORED_PATH"])
	// No pre_restore ran in this setup, so DEVM_PRE_EXIT_CODE must be
	// absent (not "0") — the shell convention is `${DEVM_PRE_EXIT_CODE:-}`
	// distinguishing "pre didn't run" from "pre ran and returned 0".
	_, present := gotEnv["DEVM_PRE_EXIT_CODE"]
	assert.False(t, present, "DEVM_PRE_EXIT_CODE must be unset when pre_restore did not run")
}

func TestRestore_PathMode_PostFailedArchiveRestoresCleanly(t *testing.T) {
	setup := makePathModeRestoreSetup(t, metaOverrides{Mode: ModePath, Status: StatusPostFailed})
	require.NoError(t, setup.run(),
		"Status doesn't gate restore — only Mode/CapturedPath do")
}

// TestRestore_NonzeroExitErrors covers the exit-code surfacing: the
// hook returned non-zero so Restore must report a failure to the
// caller.
func TestRestore_NonzeroExitErrors(t *testing.T) {
	ident := newTestIdentity(t)
	runner := &captureAndRestoreRunner{backupPayload: []byte("seed")}

	_, err := Capture(context.Background(), CaptureConfig{
		Ident:      ident,
		ProjectID:  "proj",
		EntryName:  "pg-data",
		Source:     SourceCLI,
		HookRunner: runner,
		Now:        stepClock(),
	})
	require.NoError(t, err)

	restored, err := Restore(context.Background(), RestoreConfig{
		Ident:      ident,
		ProjectID:  "proj",
		EntryName:  "pg-data",
		HookRunner: &restoreFailRunner{exit: 7},
		Entries:    entriesWith("pg-data"),
	})
	require.Error(t, err)
	assert.False(t, restored)
	assert.Contains(t, err.Error(), "7")
}

// --- M-1 / M-4: force-log drift guard and errors.Join aggregation ---

// captureLog redirects the stdlib logger's output to a buffer for the
// duration of t, restoring it on cleanup. Used by the --force log-guard
// test; the fix narrows the "bypassing drift" log line to the cases
// where drift actually exists.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	prevOut := log.Writer()
	prevFlags := log.Flags()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	return &buf
}

// TestRestore_ForceWithoutDrift_EmitsNoLog pins M-1: --force must not
// log "bypassing drift" when the archive and yaml actually agree.
// Before the fix, the log line fired on every --force restore,
// regardless of whether anything was being bypassed.
func TestRestore_ForceWithoutDrift_EmitsNoLog(t *testing.T) {
	buf := captureLog(t)
	setup := makeRestoreSetup(t, metaOverrides{Mode: ModeStream})
	setup.entry.Path = nil // yaml agrees — stream mode
	setup.cfg.Force = true
	require.NoError(t, setup.run())
	assert.NotContains(t, buf.String(), "bypassing drift",
		"--force on a drift-free restore must not emit a bypass log line")
}

// TestRestore_ForceWithDrift_StillLogs pins the complement of M-1:
// the audit log line still fires when --force actually bypasses a
// mode mismatch, so operator decisions stay visible.
func TestRestore_ForceWithDrift_StillLogs(t *testing.T) {
	buf := captureLog(t)
	setup := makeRestoreSetup(t, metaOverrides{Mode: ModeStream})
	setup.entry.Path = ptr("/foo") // yaml now says path — drift
	setup.cfg.Force = true
	require.NoError(t, setup.run())
	assert.Contains(t, buf.String(), "bypassing drift",
		"--force on real drift must log the bypass for audit")
}

// errsAllPostFailSentinel is a package-level sentinel the M-4 test uses
// so it can prove errors.Is reaches through the aggregated multi-phase
// error to the specific sentinel a phase returned.
var errsAllPostFailSentinel = errors.New("sentinel: post phase broke")

// sentinelPostRunner's RunPostRestore returns errsAllPostFailSentinel
// wrapped in a RunPostRestore transport-error signal. Paired with a
// pre_restore that also fails, this forces runPathModeRestore into its
// multi-error aggregation path.
type sentinelPostRunner struct{ pathRestoreFakeRunner }

func (r *sentinelPostRunner) RunPreRestore(ctx context.Context, entry string, env map[string]string) (int, error) {
	return 2, nil // pre failed with exit 2
}
func (r *sentinelPostRunner) RunPostRestore(ctx context.Context, entry string, env map[string]string) (int, error) {
	return 0, errsAllPostFailSentinel
}

// TestRestore_PathMode_MultiPhaseError_UnwrapsToEachSentinel pins M-4:
// when pre and post both fail, the returned error must be joinable such
// that errors.Is finds each phase's sentinel through the aggregate.
// strings.Join on err.Error() loses this (plain text, not wrapping).
func TestRestore_PathMode_MultiPhaseError_UnwrapsToEachSentinel(t *testing.T) {
	setup := makePathModeRestoreSetup(t, metaOverrides{Mode: ModePath})
	setup.hookExists = map[string]bool{"pre_restore_foo": true, "post_restore_foo": true}
	setup.runner = &pathRestoreFakeRunner{presence: setup.presence()}

	// Direct Run: bypass the setup helper's callback indirection so the
	// sentinel reaches the aggregation path verbatim.
	sent := &sentinelPostRunner{pathRestoreFakeRunner: *setup.runner}
	setup.cfg.HookRunner = sent
	setup.cfg.Path = setup.entry.Path
	var m schema.OrderedMap[schema.Snapshot]
	m.Set(setup.name, setup.entry)
	setup.cfg.Entries = m

	_, err := Restore(context.Background(), setup.cfg)
	require.Error(t, err)
	// errors.Is must reach the post phase's sentinel through the
	// joined chain — the key property errors.Join gives you and
	// strings.Join does not.
	assert.True(t, errors.Is(err, errsAllPostFailSentinel),
		"errors.Is must find the post sentinel through the joined multi-phase error: got %v", err)
}
