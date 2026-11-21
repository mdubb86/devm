package snapshot

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRunner is an in-process HookRunner used in capture tests. It
// writes a fixed payload, reports a configurable exit code, and can
// optionally delay or block on ctx so the timeout path is exercised.
type fakeRunner struct {
	write []byte
	exit  int
	delay time.Duration
	block bool

	// concurrency tracking so TestCapture_ConcurrentCalls_Serialise
	// can prove the flock actually serializes callers.
	active    *atomic.Int32
	maxActive *atomic.Int32

	mu     sync.Mutex
	called int
}

// RunRestore satisfies the HookRunner interface; capture tests never
// call it, so the stub rejects use rather than silently appearing to
// succeed.
func (r *fakeRunner) RunRestore(ctx context.Context, entryName string, stdin io.Reader) (int, error) {
	panic("fakeRunner.RunRestore: not used in capture tests")
}

// Pre/post lifecycle hooks and HookExists are consumed by path-mode
// capture/restore flows added in Task 5/6/8. Stream-mode capture tests
// never touch them; the panics stop a future change from silently
// routing through this stub.
func (r *fakeRunner) RunPreBackup(ctx context.Context, entryName string, env map[string]string) (string, int, error) {
	panic("fakeRunner.RunPreBackup: not used in capture tests")
}
func (r *fakeRunner) RunPostBackup(ctx context.Context, entryName string, env map[string]string) (int, error) {
	panic("fakeRunner.RunPostBackup: not used in capture tests")
}
func (r *fakeRunner) RunPreRestore(ctx context.Context, entryName string, env map[string]string) (int, error) {
	panic("fakeRunner.RunPreRestore: not used in capture tests")
}
func (r *fakeRunner) RunPostRestore(ctx context.Context, entryName string, env map[string]string) (int, error) {
	panic("fakeRunner.RunPostRestore: not used in capture tests")
}
func (r *fakeRunner) HookExists(ctx context.Context, entryName string) (HookPresence, error) {
	panic("fakeRunner.HookExists: not used in capture tests")
}

func (r *fakeRunner) RunBackup(ctx context.Context, entryName string, stdout io.Writer) (int, error) {
	r.mu.Lock()
	r.called++
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
	if r.block {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	if r.delay > 0 {
		select {
		case <-time.After(r.delay):
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	if len(r.write) > 0 {
		if _, err := stdout.Write(r.write); err != nil {
			return 0, err
		}
	}
	return r.exit, nil
}

// newTestIdentity points HOME at a per-test temp dir so RuntimeDir()
// paths — and therefore EntryDir() paths — land inside the test.
func newTestIdentity(t *testing.T) identity.Config {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	return identity.Config{Name: "devm-test"}
}

// stepClock returns a time.Time that advances by one second each
// call. Serialized Capture calls therefore produce distinct filename
// timestamps even though the format has only second granularity.
func stepClock() func() time.Time {
	var mu sync.Mutex
	n := int64(0)
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		v := base.Add(time.Duration(n) * time.Second)
		n++
		return v
	}
}

func readFailureLines(t *testing.T, dir string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "failures.log"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read failures.log: %v", err)
	}
	trimmed := strings.TrimRight(string(b), "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

func assertNoArchiveArtifacts(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		assert.Falsef(t, strings.HasSuffix(name, ".zst"), "unexpected archive %s", name)
		assert.Falsef(t, strings.HasSuffix(name, ".tmp"), "leftover tempfile %s", name)
		assert.Falsef(t, strings.HasSuffix(name, ".meta.json"), "unexpected meta %s", name)
	}
}

func TestCapture_HappyPath_WritesArchiveAndMeta(t *testing.T) {
	ident := newTestIdentity(t)
	runner := &fakeRunner{write: []byte("hello world"), exit: 0}
	dir := EntryDir(ident, "proj", "pg-data")

	meta, err := Capture(context.Background(), CaptureConfig{
		Ident:      ident,
		ProjectID:  "proj",
		EntryName:  "pg-data",
		Source:     SourceCLI,
		HookRunner: runner,
		Now:        stepClock(),
	})
	require.NoError(t, err)

	// Archive exists and on-disk size matches meta.SizeBytes.
	info, err := os.Stat(filepath.Join(dir, meta.TS+".zst"))
	require.NoError(t, err)
	assert.Greater(t, info.Size(), int64(0))
	assert.Equal(t, info.Size(), meta.SizeBytes)

	// Meta sidecar round-trips.
	got, err := ReadMeta(dir, meta.TS)
	require.NoError(t, err)
	assert.Equal(t, meta, got)

	// Latest symlink points at this archive.
	ts, err := LatestTS(dir)
	require.NoError(t, err)
	assert.Equal(t, meta.TS, ts)

	assert.Equal(t, SourceCLI, meta.Source)
	assert.Equal(t, 0, meta.ExitCode)
	assert.Equal(t, "pg-data", meta.ArchiveName)
	assert.Equal(t, "backup_pg-data", meta.HookName)
	assert.NotEmpty(t, meta.TS)
	assert.Equal(t, 1, runner.called)
}

func TestCapture_StreamMode_RecordsModeStream(t *testing.T) {
	ident := newTestIdentity(t)
	runner := &fakeRunner{write: []byte("hello"), exit: 0}

	m, err := Capture(context.Background(), CaptureConfig{
		Ident:      ident,
		ProjectID:  "proj",
		EntryName:  "pg-data",
		Source:     SourceCLI,
		HookRunner: runner,
		Now:        stepClock(),
	})
	require.NoError(t, err)
	assert.Equal(t, ModeStream, m.Mode)
	assert.Equal(t, StatusOk, m.Status)
	assert.Empty(t, m.CapturedPath)
}

func TestCapture_HookExitNonzero_LeavesNoArchive(t *testing.T) {
	ident := newTestIdentity(t)
	runner := &fakeRunner{write: []byte("partial"), exit: 2}
	dir := EntryDir(ident, "proj", "pg-data")

	_, err := Capture(context.Background(), CaptureConfig{
		Ident:      ident,
		ProjectID:  "proj",
		EntryName:  "pg-data",
		Source:     SourceCLI,
		HookRunner: runner,
		Now:        stepClock(),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit")

	assertNoArchiveArtifacts(t, dir)

	// latest symlink must not exist on a never-captured entry.
	latest, err := LatestTS(dir)
	require.NoError(t, err)
	assert.Equal(t, "", latest)

	lines := readFailureLines(t, dir)
	assert.Len(t, lines, 1)
}

func TestCapture_EmptyStream_FinalisesArchive(t *testing.T) {
	ident := newTestIdentity(t)
	runner := &fakeRunner{write: nil, exit: 0}
	dir := EntryDir(ident, "proj", "pg-data")

	meta, err := Capture(context.Background(), CaptureConfig{
		Ident:      ident,
		ProjectID:  "proj",
		EntryName:  "pg-data",
		Source:     SourceCLI,
		HookRunner: runner,
		Now:        stepClock(),
	})
	require.NoError(t, err)

	info, err := os.Stat(filepath.Join(dir, meta.TS+".zst"))
	require.NoError(t, err)
	// zstd writes a small frame header/trailer even for empty input.
	assert.Greater(t, info.Size(), int64(0))
	assert.Equal(t, info.Size(), meta.SizeBytes)

	ts, err := LatestTS(dir)
	require.NoError(t, err)
	assert.Equal(t, meta.TS, ts)
}

func TestCapture_TimeoutFiresMidStream_CleansUp(t *testing.T) {
	ident := newTestIdentity(t)
	runner := &fakeRunner{block: true}
	dir := EntryDir(ident, "proj", "pg-data")

	_, err := Capture(context.Background(), CaptureConfig{
		Ident:       ident,
		ProjectID:   "proj",
		EntryName:   "pg-data",
		Source:      SourceCLI,
		StepTimeout: 50 * time.Millisecond,
		HookRunner:  runner,
		Now:         stepClock(),
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	assertNoArchiveArtifacts(t, dir)

	latest, err := LatestTS(dir)
	require.NoError(t, err)
	assert.Equal(t, "", latest)

	lines := readFailureLines(t, dir)
	assert.Len(t, lines, 1)
}

func TestCapture_FailingNewCapture_PreservesOldArchive(t *testing.T) {
	ident := newTestIdentity(t)
	dir := EntryDir(ident, "proj", "pg-data")
	clock := stepClock()

	runnerOK := &fakeRunner{write: []byte("snapshot-1"), exit: 0}
	first, err := Capture(context.Background(), CaptureConfig{
		Ident:      ident,
		ProjectID:  "proj",
		EntryName:  "pg-data",
		Source:     SourceCLI,
		Keep:       1,
		HookRunner: runnerOK,
		Now:        clock,
	})
	require.NoError(t, err)

	runnerFail := &fakeRunner{write: []byte("snapshot-2"), exit: 2}
	_, err = Capture(context.Background(), CaptureConfig{
		Ident:      ident,
		ProjectID:  "proj",
		EntryName:  "pg-data",
		Source:     SourceCLI,
		Keep:       1,
		HookRunner: runnerFail,
		Now:        clock,
	})
	require.Error(t, err)

	_, err = os.Stat(filepath.Join(dir, first.TS+".zst"))
	require.NoError(t, err, "first archive must survive a subsequent failure")

	latest, err := LatestTS(dir)
	require.NoError(t, err)
	assert.Equal(t, first.TS, latest, "latest must still point at the first archive")
}

// TestCapture_PostRenameFailure_RemovesArchiveAndLogsFailure covers
// the "finalise step blew up after the archive already landed on
// disk" path. The obstruction here is a pre-planted directory where
// WriteMeta wants to write its temp file — opening a directory
// O_WRONLY returns EISDIR, so WriteMeta fails and Capture must clean
// up its orphaned archive and log the failure.
func TestCapture_PostRenameFailure_RemovesArchiveAndLogsFailure(t *testing.T) {
	ident := newTestIdentity(t)
	dir := EntryDir(ident, "proj", "pg-data")
	require.NoError(t, os.MkdirAll(dir, 0755))

	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	ts := NewTimestamp(base)
	// Pre-plant a directory at WriteMeta's .tmp target.
	require.NoError(t, os.Mkdir(filepath.Join(dir, ts+".meta.json.tmp"), 0755))

	runner := &fakeRunner{write: []byte("hello"), exit: 0}
	_, err := Capture(context.Background(), CaptureConfig{
		Ident:      ident,
		ProjectID:  "proj",
		EntryName:  "pg-data",
		Source:     SourceCLI,
		HookRunner: runner,
		Now:        func() time.Time { return base },
	})
	require.Error(t, err)

	_, statErr := os.Stat(filepath.Join(dir, ts+".zst"))
	assert.Truef(t, os.IsNotExist(statErr), "orphaned archive remains: %v", statErr)

	_, statErr = os.Stat(filepath.Join(dir, ts+".meta.json"))
	assert.Truef(t, os.IsNotExist(statErr), "orphaned meta sidecar: %v", statErr)

	latest, err := LatestTS(dir)
	require.NoError(t, err)
	assert.Equal(t, "", latest)

	lines := readFailureLines(t, dir)
	assert.Len(t, lines, 1)
}

// --- Path-mode test harness ---

// pathFakeRunner is a HookRunner used by path-mode Capture tests.
// presence drives HookExists so individual tests opt pre/post hooks
// in. The three Stream/Restore methods and the Pre/PostRestore
// methods panic so a future change can't silently route through this
// stub.
type pathFakeRunner struct {
	presence   HookPresence
	preBackup  func(name string, env map[string]string) (string, int, error)
	postBackup func(name string, env map[string]string) (int, error)

	preBackupCalls  int
	preBackupEnv    map[string]string
	postBackupCalls int
	postBackupEnv   map[string]string
}

func (r *pathFakeRunner) RunBackup(ctx context.Context, entryName string, stdout io.Writer) (int, error) {
	panic("pathFakeRunner.RunBackup: not used in path-mode tests")
}
func (r *pathFakeRunner) RunRestore(ctx context.Context, entryName string, stdin io.Reader) (int, error) {
	panic("pathFakeRunner.RunRestore: not used in path-mode tests")
}
func (r *pathFakeRunner) RunPreBackup(ctx context.Context, entryName string, env map[string]string) (string, int, error) {
	r.preBackupCalls++
	r.preBackupEnv = env
	if r.preBackup == nil {
		return "", 0, nil
	}
	return r.preBackup(entryName, env)
}
func (r *pathFakeRunner) RunPostBackup(ctx context.Context, entryName string, env map[string]string) (int, error) {
	r.postBackupCalls++
	r.postBackupEnv = env
	if r.postBackup == nil {
		return 0, nil
	}
	return r.postBackup(entryName, env)
}
func (r *pathFakeRunner) RunPreRestore(ctx context.Context, entryName string, env map[string]string) (int, error) {
	panic("pathFakeRunner.RunPreRestore: not used in path-mode capture tests")
}
func (r *pathFakeRunner) RunPostRestore(ctx context.Context, entryName string, env map[string]string) (int, error) {
	panic("pathFakeRunner.RunPostRestore: not used in path-mode capture tests")
}
func (r *pathFakeRunner) HookExists(ctx context.Context, entryName string) (HookPresence, error) {
	return r.presence, nil
}

// fakeTarRunner stubs the TarRunner for capture tests. RunRestoreTar
// panics because Task 8 will exercise it separately.
//
// statStub lets a test program StatPath's answer. When nil the fake
// falls back to a Mac-side os.Stat + filepath.EvalSymlinks on the given
// path, which matches the semantics the earlier capture code applied
// directly to Mac temp dirs — the path-mode tests still exercise real
// Mac paths and get the same answers through the fake.
type fakeTarRunner struct {
	backup   func(entry, source, basename string, isDir bool, w io.Writer) (int, error)
	statStub func(entry, guestPath string) (exists bool, isDir bool, resolvedPath string, err error)
}

func (f *fakeTarRunner) RunBackupTar(ctx context.Context, entryName, source, declaredBasename string, isDir bool, stdout io.Writer) (int, error) {
	if f.backup == nil {
		return 0, nil
	}
	return f.backup(entryName, source, declaredBasename, isDir, stdout)
}
func (f *fakeTarRunner) RunRestoreTar(ctx context.Context, entryName, declaredPath string, isDir bool, stdin io.Reader) (int, error) {
	panic("fakeTarRunner.RunRestoreTar: not used in capture tests")
}
func (f *fakeTarRunner) StatPath(ctx context.Context, entryName, guestPath string) (bool, bool, string, error) {
	if f.statStub != nil {
		return f.statStub(entryName, guestPath)
	}
	info, err := os.Stat(guestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, false, "", nil
		}
		return false, false, "", err
	}
	resolved, err := filepath.EvalSymlinks(guestPath)
	if err != nil {
		return false, false, "", err
	}
	return true, info.IsDir(), resolved, nil
}

// captureFakeOptions configures a one-shot path-mode Capture call.
// hookExists keys use the fully-qualified function name so tests
// declare them the same way `declare -F` would surface them.
type captureFakeOptions struct {
	path       *string
	entryName  string
	source     Source
	hookExists map[string]bool
	preBackup  func(name string, env map[string]string) (string, int, error)
	postBackup func(name string, env map[string]string) (int, error)
	tarStub    func(entry, source, basename string, isDir bool, w io.Writer) (int, error)
	statStub   func(entry, guestPath string) (exists bool, isDir bool, resolvedPath string, err error)
}

func (o captureFakeOptions) entry() string {
	if o.entryName == "" {
		return "foo"
	}
	return o.entryName
}

func (o captureFakeOptions) presence() HookPresence {
	e := o.entry()
	return HookPresence{
		Backup:      o.hookExists["backup_"+e],
		Restore:     o.hookExists["restore_"+e],
		PreBackup:   o.hookExists["pre_backup_"+e],
		PostBackup:  o.hookExists["post_backup_"+e],
		PreRestore:  o.hookExists["pre_restore_"+e],
		PostRestore: o.hookExists["post_restore_"+e],
	}
}

func runCaptureWithFakeErr(t *testing.T, opts captureFakeOptions) (Meta, error) {
	t.Helper()
	ident := newTestIdentity(t)
	runner := &pathFakeRunner{
		presence:   opts.presence(),
		preBackup:  opts.preBackup,
		postBackup: opts.postBackup,
	}
	tr := &fakeTarRunner{backup: opts.tarStub, statStub: opts.statStub}
	src := opts.source
	if src == "" {
		src = SourceCLI
	}
	return Capture(context.Background(), CaptureConfig{
		Ident:      ident,
		ProjectID:  "proj",
		EntryName:  opts.entry(),
		Source:     src,
		Path:       opts.path,
		HookRunner: runner,
		TarRunner:  tr,
		Now:        stepClock(),
	})
}

func runCaptureWithFake(t *testing.T, opts captureFakeOptions) Meta {
	t.Helper()
	m, err := runCaptureWithFakeErr(t, opts)
	require.NoError(t, err)
	return m
}

func TestCapture_PathMode_NoHook_TarsDeclaredPath(t *testing.T) {
	src := t.TempDir()
	srcResolved, err := filepath.EvalSymlinks(src)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(src, "x.txt"), []byte("hi"), 0o644))

	var gotSource string
	var gotIsDir bool
	m := runCaptureWithFake(t, captureFakeOptions{
		path: &src,
		tarStub: func(entry, source, basename string, isDir bool, w io.Writer) (int, error) {
			gotSource = source
			gotIsDir = isDir
			_, _ = w.Write([]byte("TAR_BYTES"))
			return 0, nil
		},
	})
	assert.Equal(t, ModePath, m.Mode)
	assert.Equal(t, srcResolved, m.CapturedPath)
	assert.Equal(t, StatusOk, m.Status)
	assert.True(t, m.IsDir)
	assert.Equal(t, srcResolved, gotSource)
	assert.True(t, gotIsDir)
}

func TestCapture_PathMode_PreHookEchoesOverride(t *testing.T) {
	src := t.TempDir()
	override := filepath.Join(t.TempDir(), "staging")
	require.NoError(t, os.MkdirAll(override, 0o755))
	overrideResolved, err := filepath.EvalSymlinks(override)
	require.NoError(t, err)

	var capturedSource string
	m := runCaptureWithFake(t, captureFakeOptions{
		path:       &src,
		hookExists: map[string]bool{"pre_backup_foo": true},
		preBackup: func(name string, env map[string]string) (string, int, error) {
			assert.Equal(t, "foo", name)
			assert.Equal(t, "foo", env["DEVM_SNAPSHOT_NAME"])
			assert.Equal(t, src, env["DEVM_SNAPSHOT_PATH"])
			return "file://" + override, 0, nil
		},
		tarStub: func(entry, source, basename string, isDir bool, w io.Writer) (int, error) {
			capturedSource = source
			return 0, nil
		},
	})
	assert.Equal(t, overrideResolved, m.CapturedPath)
	assert.Equal(t, overrideResolved, capturedSource)
}

func TestCapture_PathMode_PreHookExtraStdoutLines_Rejected(t *testing.T) {
	src := t.TempDir()
	_, err := runCaptureWithFakeErr(t, captureFakeOptions{
		path:       &src,
		hookExists: map[string]bool{"pre_backup_foo": true},
		preBackup: func(name string, env map[string]string) (string, int, error) {
			return "stopping daemon\nfile:///tmp/x", 0, nil
		},
	})
	require.ErrorContains(t, err, "pre_backup_foo: stdout must be empty or a single file:/// line")
}

func TestCapture_PathMode_PreHookWrongScheme_Rejected(t *testing.T) {
	src := t.TempDir()
	_, err := runCaptureWithFakeErr(t, captureFakeOptions{
		path:       &src,
		hookExists: map[string]bool{"pre_backup_foo": true},
		preBackup: func(name string, env map[string]string) (string, int, error) {
			// Two slashes instead of three — spec is strict.
			return "file://tmp/x", 0, nil
		},
	})
	require.ErrorContains(t, err, "pre_backup_foo: stdout must be empty or a single file:/// line")
}

func TestCapture_PathMode_PreHookNonzero_SkipsTar(t *testing.T) {
	src := t.TempDir()
	tarCalled := false
	_, err := runCaptureWithFakeErr(t, captureFakeOptions{
		path:       &src,
		hookExists: map[string]bool{"pre_backup_foo": true},
		preBackup:  func(name string, env map[string]string) (string, int, error) { return "", 2, nil },
		tarStub: func(entry, source, basename string, isDir bool, w io.Writer) (int, error) {
			tarCalled = true
			return 0, nil
		},
	})
	require.Error(t, err)
	assert.False(t, tarCalled, "tar must not run when pre_backup exits non-zero")
}

func TestCapture_PathMode_CaptureTargetMissing_FailsLoud(t *testing.T) {
	missing := "/does/not/exist/devm-snapshot-test"
	_, err := runCaptureWithFakeErr(t, captureFakeOptions{
		path: &missing,
	})
	require.ErrorContains(t, err, missing)
	require.ErrorContains(t, err, "does not exist")
}

func TestCapture_PathMode_FileSource_UsesFileBranch(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, ".gitconfig")
	require.NoError(t, os.WriteFile(f, []byte("[user] name = me\n"), 0o644))

	var gotIsDir *bool
	var gotBasename string
	_ = runCaptureWithFake(t, captureFakeOptions{
		path: &f,
		tarStub: func(entry, source, basename string, isDir bool, w io.Writer) (int, error) {
			gotIsDir = &isDir
			gotBasename = basename
			return 0, nil
		},
	})
	require.NotNil(t, gotIsDir)
	assert.False(t, *gotIsDir)
	assert.Equal(t, ".gitconfig", gotBasename)
}

func TestCapture_PathMode_SymlinkPath_CapturesTarget(t *testing.T) {
	real := t.TempDir()
	realResolved, err := filepath.EvalSymlinks(real)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(real, "payload"), []byte("x"), 0o644))

	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(real, link))

	var gotSource string
	var gotIsDir bool
	m := runCaptureWithFake(t, captureFakeOptions{
		path: &link,
		tarStub: func(entry, source, basename string, isDir bool, w io.Writer) (int, error) {
			gotSource, gotIsDir = source, isDir
			return 0, nil
		},
	})
	assert.Equal(t, realResolved, gotSource)
	assert.True(t, gotIsDir)
	assert.Equal(t, realResolved, m.CapturedPath)
}

func TestCapture_PathMode_TarRunnerRequired(t *testing.T) {
	src := t.TempDir()
	runner := &pathFakeRunner{}
	_, err := Capture(context.Background(), CaptureConfig{
		Ident:      newTestIdentity(t),
		ProjectID:  "proj",
		EntryName:  "foo",
		Source:     SourceCLI,
		Path:       &src,
		HookRunner: runner,
		Now:        stepClock(),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TarRunner is required")
}

func TestCapture_PathMode_RoundTripMetaIncludesPathFields(t *testing.T) {
	// Meta.CapturedPath, Meta.IsDir and Meta.Mode must survive a
	// sidecar write/read cycle — the drift check in Task 7 reads
	// them back from disk.
	src := t.TempDir()
	srcResolved, err := filepath.EvalSymlinks(src)
	require.NoError(t, err)

	ident := newTestIdentity(t)
	tr := &fakeTarRunner{}
	m, err := Capture(context.Background(), CaptureConfig{
		Ident:      ident,
		ProjectID:  "proj",
		EntryName:  "foo",
		Source:     SourceCLI,
		Path:       &src,
		HookRunner: &pathFakeRunner{},
		TarRunner:  tr,
		Now:        stepClock(),
	})
	require.NoError(t, err)

	dir := EntryDir(ident, "proj", "foo")
	got, err := ReadMeta(dir, m.TS)
	require.NoError(t, err)
	assert.Equal(t, ModePath, got.Mode)
	assert.Equal(t, srcResolved, got.CapturedPath)
	assert.True(t, got.IsDir)
	assert.Equal(t, StatusOk, got.Status)
}

// ptr returns a pointer to v. Path-mode tests take *string because
// Path on CaptureConfig is nullable — `&s` inline is noisier when the
// value is a t.TempDir() call chain.
func ptr[T any](v T) *T { return &v }

// stubTarSuccess is a TarRunner stub that reports success without
// writing bytes. Path-mode post-hook tests do not care what is in the
// archive; they just need the tar step to succeed so the post step
// gets a chance to run.
func stubTarSuccess(entry, source, basename string, isDir bool, w io.Writer) (int, error) {
	return 0, nil
}

// snapshotDirFor reconstructs the per-entry snapshot directory using
// the same (ident, projectID) that runCaptureWithFakeErr builds
// internally. newTestIdentity sets $HOME via t.Setenv so RuntimeDir()
// resolves back to the same path here.
func snapshotDirFor(t *testing.T, entry string) string {
	t.Helper()
	return EntryDir(identity.Config{Name: "devm-test"}, "proj", entry)
}

func TestCapture_PathMode_PostBackupFails_ArchiveKept_StatusPostFailed(t *testing.T) {
	src := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(src, "x"), []byte("hi"), 0o644))
	m, err := runCaptureWithFakeErr(t, captureFakeOptions{
		path:       &src,
		hookExists: map[string]bool{"post_backup_foo": true},
		postBackup: func(name string, env map[string]string) (int, error) { return 1, nil },
		tarStub:    stubTarSuccess,
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "post_backup_foo exit 1")
	assert.NotEmpty(t, m.TS)
	assert.Equal(t, StatusPostFailed, m.Status)

	// Archive must still exist on disk.
	dir := snapshotDirFor(t, "foo")
	_, statErr := os.Stat(filepath.Join(dir, m.TS+".zst"))
	require.NoError(t, statErr)

	// Status on the sidecar must also reflect post_failed so a later
	// reader (list/restore/drift) sees the same truth.
	got, err := ReadMeta(dir, m.TS)
	require.NoError(t, err)
	assert.Equal(t, StatusPostFailed, got.Status)
}

// After a capture lands as post_failed, the latest symlink must keep
// pointing at the previous ok archive — operators restoring the
// default handle must never get a flagged archive by surprise.
func TestCapture_PostFailed_LatestSymlinkPointsToPrevOk(t *testing.T) {
	ident := newTestIdentity(t)
	dir := EntryDir(ident, "proj", "foo")
	clock := stepClock()

	src := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(src, "x"), []byte("hi"), 0o644))

	tr := &fakeTarRunner{backup: stubTarSuccess}

	// First capture: happy path, Status=ok.
	firstMeta, err := Capture(context.Background(), CaptureConfig{
		Ident:      ident,
		ProjectID:  "proj",
		EntryName:  "foo",
		Source:     SourceCLI,
		Path:       &src,
		HookRunner: &pathFakeRunner{},
		TarRunner:  tr,
		Now:        clock,
	})
	require.NoError(t, err)
	require.Equal(t, StatusOk, firstMeta.Status)

	latest, err := LatestTS(dir)
	require.NoError(t, err)
	require.Equal(t, firstMeta.TS, latest)

	// Second capture: post_backup fails -> Status=post_failed.
	runnerPostFail := &pathFakeRunner{
		presence:   HookPresence{PostBackup: true},
		postBackup: func(name string, env map[string]string) (int, error) { return 1, nil },
	}
	secondMeta, err := Capture(context.Background(), CaptureConfig{
		Ident:      ident,
		ProjectID:  "proj",
		EntryName:  "foo",
		Source:     SourceCLI,
		Path:       &src,
		HookRunner: runnerPostFail,
		TarRunner:  tr,
		Now:        clock,
	})
	require.Error(t, err)
	require.Equal(t, StatusPostFailed, secondMeta.Status)
	require.NotEqual(t, firstMeta.TS, secondMeta.TS)

	// Latest still points at the earlier ok archive.
	latest, err = LatestTS(dir)
	require.NoError(t, err)
	assert.Equal(t, firstMeta.TS, latest)
}

func TestCapture_PathMode_PostBackup_SeesPreExitCode(t *testing.T) {
	var gotEnv map[string]string
	_, _ = runCaptureWithFakeErr(t, captureFakeOptions{
		path:       ptr(t.TempDir()),
		hookExists: map[string]bool{"pre_backup_foo": true, "post_backup_foo": true},
		preBackup:  func(name string, env map[string]string) (string, int, error) { return "", 0, nil },
		postBackup: func(name string, env map[string]string) (int, error) { gotEnv = env; return 0, nil },
		tarStub:    stubTarSuccess,
	})
	assert.Equal(t, "0", gotEnv["DEVM_PRE_EXIT_CODE"])
}

func TestCapture_PathMode_NoPreHook_PostSeesPreExitCodeUnset(t *testing.T) {
	var gotEnv map[string]string
	_, _ = runCaptureWithFakeErr(t, captureFakeOptions{
		path:       ptr(t.TempDir()),
		hookExists: map[string]bool{"post_backup_foo": true},
		postBackup: func(name string, env map[string]string) (int, error) { gotEnv = env; return 0, nil },
		tarStub:    stubTarSuccess,
	})
	_, present := gotEnv["DEVM_PRE_EXIT_CODE"]
	assert.False(t, present)
}

// TestCapture_PathMode_PostAfterCaptureFailure_LogsAndReturnsCaptureErr
// covers the finalizer-after-failure path. pre is non-zero so tar is
// skipped. post still runs (finalizer semantics) and reports its own
// non-zero exit. The returned error must be the capture (pre) failure
// — the post failure is logged, not substituted for the root cause.
func TestCapture_PathMode_PostAfterCaptureFailure_LogsAndReturnsCaptureErr(t *testing.T) {
	src := t.TempDir()
	_, err := runCaptureWithFakeErr(t, captureFakeOptions{
		path:       &src,
		hookExists: map[string]bool{"pre_backup_foo": true, "post_backup_foo": true},
		preBackup:  func(name string, env map[string]string) (string, int, error) { return "", 3, nil },
		postBackup: func(name string, env map[string]string) (int, error) { return 7, nil },
		tarStub:    stubTarSuccess,
	})
	require.Error(t, err)
	// Root cause stays the pre failure (capture error).
	assert.Contains(t, err.Error(), "pre_backup_foo")
	assert.NotContains(t, err.Error(), "post_backup_foo exit 7")

	// failures.log carries BOTH entries: the capture failure AND the
	// post-after-capture-failure entry, with the second's reason
	// clearly marked.
	dir := snapshotDirFor(t, "foo")
	lines := readFailureLines(t, dir)
	require.Len(t, lines, 2)
	assert.Contains(t, lines[1], "post_backup_foo exit 7 after capture failure")
}

func TestCapture_ConcurrentCalls_Serialise(t *testing.T) {
	ident := newTestIdentity(t)
	dir := EntryDir(ident, "proj", "pg-data")
	clock := stepClock()

	var active, maxActive atomic.Int32

	var wg sync.WaitGroup
	results := make(chan string, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runner := &fakeRunner{
				write:     []byte("payload"),
				exit:      0,
				delay:     30 * time.Millisecond,
				active:    &active,
				maxActive: &maxActive,
			}
			m, err := Capture(context.Background(), CaptureConfig{
				Ident:      ident,
				ProjectID:  "proj",
				EntryName:  "pg-data",
				Source:     SourceCLI,
				HookRunner: runner,
				Now:        clock,
			})
			if err != nil {
				errs <- err
				return
			}
			results <- m.TS
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	for e := range errs {
		require.NoError(t, e)
	}
	var stamps []string
	for s := range results {
		stamps = append(stamps, s)
	}
	require.Len(t, stamps, 2)
	assert.NotEqual(t, stamps[0], stamps[1])

	for _, s := range stamps {
		_, err := os.Stat(filepath.Join(dir, s+".zst"))
		require.NoError(t, err)
	}

	// Lock proven: never two hooks running at once.
	assert.Equal(t, int32(1), maxActive.Load(), "flock must serialize concurrent captures")

	// Latest points at the later-finalized (= later timestamp) archive.
	sort.Strings(stamps)
	latest, err := LatestTS(dir)
	require.NoError(t, err)
	assert.Equal(t, stamps[1], latest)
}

// --- Per-phase timeout and finalizer-context fakes (I-1/I-2/M-3) ---

// phaseRecordingRunner captures ctx state at each hook invocation so
// per-phase timeout tests can prove that pre / step / post contexts
// are independent — each derived from the caller's parent, not from a
// sibling phase.
type phaseRecordingRunner struct {
	presence HookPresence

	preCtxErr    error
	preDeadline  time.Time
	preHadDL     bool
	preBlockDone bool
	preBackup    func(ctx context.Context, name string, env map[string]string) (string, int, error)

	postCtxErr    error
	postDeadline  time.Time
	postHadDL     bool
	postInvoked   bool
	postBackup    func(ctx context.Context, name string, env map[string]string) (int, error)
	postInvokedAt time.Time
}

func (r *phaseRecordingRunner) RunBackup(ctx context.Context, entryName string, stdout io.Writer) (int, error) {
	panic("phaseRecordingRunner.RunBackup: not used")
}
func (r *phaseRecordingRunner) RunRestore(ctx context.Context, entryName string, stdin io.Reader) (int, error) {
	panic("phaseRecordingRunner.RunRestore: not used")
}
func (r *phaseRecordingRunner) RunPreBackup(ctx context.Context, entryName string, env map[string]string) (string, int, error) {
	r.preCtxErr = ctx.Err()
	r.preDeadline, r.preHadDL = ctx.Deadline()
	if r.preBackup == nil {
		return "", 0, nil
	}
	return r.preBackup(ctx, entryName, env)
}
func (r *phaseRecordingRunner) RunPostBackup(ctx context.Context, entryName string, env map[string]string) (int, error) {
	r.postInvoked = true
	r.postInvokedAt = time.Now()
	r.postCtxErr = ctx.Err()
	r.postDeadline, r.postHadDL = ctx.Deadline()
	if r.postBackup == nil {
		return 0, nil
	}
	return r.postBackup(ctx, entryName, env)
}
func (r *phaseRecordingRunner) RunPreRestore(ctx context.Context, entryName string, env map[string]string) (int, error) {
	panic("not used")
}
func (r *phaseRecordingRunner) RunPostRestore(ctx context.Context, entryName string, env map[string]string) (int, error) {
	panic("not used")
}
func (r *phaseRecordingRunner) HookExists(ctx context.Context, entryName string) (HookPresence, error) {
	return r.presence, nil
}

// phaseRecordingTarRunner captures the tar-step ctx so a step-timeout
// test can wait on ctx.Done() and surface the deadline it saw.
type phaseRecordingTarRunner struct {
	backup func(ctx context.Context, source, basename string, isDir bool, w io.Writer) (int, error)

	stepCtxErr   error
	stepDeadline time.Time
	stepHadDL    bool
}

func (r *phaseRecordingTarRunner) RunBackupTar(ctx context.Context, entryName, source, declaredBasename string, isDir bool, stdout io.Writer) (int, error) {
	r.stepCtxErr = ctx.Err()
	r.stepDeadline, r.stepHadDL = ctx.Deadline()
	if r.backup == nil {
		return 0, nil
	}
	return r.backup(ctx, source, declaredBasename, isDir, stdout)
}
func (r *phaseRecordingTarRunner) RunRestoreTar(ctx context.Context, entryName, declaredPath string, isDir bool, stdin io.Reader) (int, error) {
	panic("not used")
}
func (r *phaseRecordingTarRunner) StatPath(ctx context.Context, entryName, guestPath string) (bool, bool, string, error) {
	// Treat the declared path as an existing dir so capture reaches tar.
	return true, true, guestPath, nil
}

// TestCapture_PathMode_PerPhaseTimeouts_IndependentDeadlines pins I-1:
// each of pre / step / post must carry its own deadline derived from
// the caller's parent context, so one phase burning through its budget
// never shortens the next. A bug that chained them (e.g. pre deriving
// from a step-bounded ctx) would make step's deadline no later than
// pre's — this test fails loud in that case.
func TestCapture_PathMode_PerPhaseTimeouts_IndependentDeadlines(t *testing.T) {
	src := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(src, "x"), []byte("hi"), 0o644))

	ident := newTestIdentity(t)
	runner := &phaseRecordingRunner{
		presence: HookPresence{PreBackup: true, PostBackup: true},
	}
	tr := &phaseRecordingTarRunner{}

	preTO, stepTO, postTO := 200*time.Millisecond, 2*time.Second, 500*time.Millisecond
	start := time.Now()
	_, err := Capture(context.Background(), CaptureConfig{
		Ident:       ident,
		ProjectID:   "proj",
		EntryName:   "foo",
		Source:      SourceCLI,
		Path:        &src,
		HookRunner:  runner,
		TarRunner:   tr,
		PreTimeout:  preTO,
		StepTimeout: stepTO,
		PostTimeout: postTO,
		Now:         stepClock(),
	})
	require.NoError(t, err)

	require.True(t, runner.preHadDL, "pre ctx must have a deadline")
	require.True(t, tr.stepHadDL, "step ctx must have a deadline")
	require.True(t, runner.postHadDL, "post ctx must have a deadline")

	// Each deadline is roughly its own configured budget from the
	// caller's start — not from some prior phase's start. The slop
	// tolerates CI jitter; the invariant is that step's deadline sits
	// at caller-start + StepTimeout (not pre's shorter bound) and
	// post's deadline sits at post-invocation + PostTimeout (not
	// step's longer bound inherited whole).
	assert.WithinDuration(t, start.Add(preTO), runner.preDeadline, 150*time.Millisecond)
	assert.WithinDuration(t, start.Add(stepTO), tr.stepDeadline, 300*time.Millisecond)
	assert.True(t, tr.stepDeadline.After(runner.preDeadline),
		"step deadline must be independent of pre's shorter bound")
	// Post's deadline measured from its own invocation is ~PostTimeout.
	// If post had inherited the step ctx whole, deadline-invocation
	// would equal step's remaining budget (≈ stepTO), not postTO.
	assert.WithinDuration(t, runner.postInvokedAt.Add(postTO), runner.postDeadline, 200*time.Millisecond,
		"post deadline must come from PostTimeout at invocation, not the step bound")
}

// TestCapture_PathMode_StepTimeout_PostFinalizerGetsFreshCtx pins I-2:
// when the step phase's context fires (tar sits past the step
// deadline), the post-finalizer must still be invoked with a FRESH,
// un-cancelled context. A bug that passed the cancelled step ctx to
// post would make exec refuse to start — this test asserts post sees
// ctx.Err() == nil at call time.
func TestCapture_PathMode_StepTimeout_PostFinalizerGetsFreshCtx(t *testing.T) {
	src := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(src, "x"), []byte("hi"), 0o644))

	ident := newTestIdentity(t)
	runner := &phaseRecordingRunner{
		presence: HookPresence{PostBackup: true},
	}
	tr := &phaseRecordingTarRunner{
		backup: func(ctx context.Context, source, basename string, isDir bool, w io.Writer) (int, error) {
			// Simulate a tar that hangs past its deadline — StepTimeout
			// fires and cancels this ctx.
			<-ctx.Done()
			return 0, ctx.Err()
		},
	}

	_, err := Capture(context.Background(), CaptureConfig{
		Ident:       ident,
		ProjectID:   "proj",
		EntryName:   "foo",
		Source:      SourceCLI,
		Path:        &src,
		HookRunner:  runner,
		TarRunner:   tr,
		StepTimeout: 50 * time.Millisecond,
		PostTimeout: 1 * time.Second,
		Now:         stepClock(),
	})
	require.Error(t, err, "step timed out, capture must fail")

	require.True(t, runner.postInvoked, "post_backup must still run after a step timeout — it is a finalizer")
	assert.NoError(t, runner.postCtxErr,
		"post must run with a fresh ctx; got Err=%v", runner.postCtxErr)
	require.True(t, runner.postHadDL, "post ctx must carry its own deadline")
	// Post's deadline measured from its own invocation ≈ PostTimeout.
	// If post had inherited the step ctx, deadline would be in the
	// past (step already fired), not ~1s out.
	assert.WithinDuration(t, runner.postInvokedAt.Add(time.Second), runner.postDeadline, 200*time.Millisecond,
		"post must get a fresh deadline derived from PostTimeout, not the fired step bound")
}

// TestCapture_PathMode_PreTransportError_PostSeesPreExitCodeUnset pins
// M-3: a transport error (perr != nil) on pre_backup means the hook did
// not run cleanly to a numeric exit, so DEVM_PRE_EXIT_CODE must stay
// UNSET in post's env — the "pre did not run" case the spec
// distinguishes from "pre ran and returned 0".
func TestCapture_PathMode_PreTransportError_PostSeesPreExitCodeUnset(t *testing.T) {
	src := t.TempDir()
	var gotEnv map[string]string
	var postInvoked bool
	_, err := runCaptureWithFakeErr(t, captureFakeOptions{
		path:       &src,
		hookExists: map[string]bool{"pre_backup_foo": true, "post_backup_foo": true},
		preBackup: func(name string, env map[string]string) (string, int, error) {
			// Non-nil err simulates a transport (exec / SSH) failure —
			// the hook did not return a shell exit code.
			return "", 0, errors.New("tart exec: broken pipe")
		},
		postBackup: func(name string, env map[string]string) (int, error) {
			postInvoked = true
			gotEnv = env
			return 0, nil
		},
	})
	require.Error(t, err)
	require.True(t, postInvoked, "post_backup must still run after pre transport failure")
	_, present := gotEnv["DEVM_PRE_EXIT_CODE"]
	assert.False(t, present, "DEVM_PRE_EXIT_CODE must be unset on a pre transport error")
}
