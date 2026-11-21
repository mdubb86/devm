package serviceapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/snapshot"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedArchive writes a zero-byte <ts>.zst + its meta sidecar + points
// latest at it, mirroring the on-disk shape Capture would leave. The
// handler tests use this to exercise list/prune/show without spinning
// real captures (each of which consumes one second of wall-clock from
// the archive-name resolution).
func seedArchive(t *testing.T, dir, ts, hookName string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ts+".zst"), []byte{0}, 0o644))
	require.NoError(t, snapshot.WriteMeta(dir, ts, snapshot.Meta{
		TS: ts, HookName: hookName, ArchiveName: hookName, Source: snapshot.SourceCLI, SizeBytes: 1,
	}))
	require.NoError(t, snapshot.UpdateLatest(dir, ts))
}

// fakeHookRunner is a drop-in HookRunner the handler tests inject via
// newHookRunnerFn. RunBackup writes a fixed payload to w; RunRestore
// drains stdin into a sink so a restore test can inspect what bytes
// arrived.
type fakeHookRunner struct {
	backupPayload []byte
	backupExit    int
	backupErr     error

	restoreSink *bytes.Buffer
	restoreExit int
	restoreErr  error

	lastEntry string
}

func (f *fakeHookRunner) RunBackup(_ context.Context, entry string, w io.Writer) (int, error) {
	f.lastEntry = entry
	if f.backupErr != nil {
		return f.backupExit, f.backupErr
	}
	if len(f.backupPayload) > 0 {
		if _, err := w.Write(f.backupPayload); err != nil {
			return 0, err
		}
	}
	return f.backupExit, nil
}

func (f *fakeHookRunner) RunRestore(_ context.Context, entry string, stdin io.Reader) (int, error) {
	f.lastEntry = entry
	if f.restoreSink != nil {
		if _, err := io.Copy(f.restoreSink, stdin); err != nil {
			return 0, err
		}
	}
	if f.restoreErr != nil {
		return f.restoreExit, f.restoreErr
	}
	return f.restoreExit, nil
}

// Pre/post lifecycle hooks and HookExists are the path-mode surface
// added in Task 5/6/8. Stream-mode handler tests don't exercise them;
// the panics stop a future change from silently going through this
// stub.
func (f *fakeHookRunner) RunPreBackup(_ context.Context, entry string, env map[string]string) (string, int, error) {
	panic("fakeHookRunner.RunPreBackup: not used in these tests")
}
func (f *fakeHookRunner) RunPostBackup(_ context.Context, entry string, env map[string]string) (int, error) {
	panic("fakeHookRunner.RunPostBackup: not used in these tests")
}
func (f *fakeHookRunner) RunPreRestore(_ context.Context, entry string, env map[string]string) (int, error) {
	panic("fakeHookRunner.RunPreRestore: not used in these tests")
}
func (f *fakeHookRunner) RunPostRestore(_ context.Context, entry string, env map[string]string) (int, error) {
	panic("fakeHookRunner.RunPostRestore: not used in these tests")
}
func (f *fakeHookRunner) HookExists(_ context.Context, entry string) (snapshot.HookPresence, error) {
	panic("fakeHookRunner.HookExists: not used in these tests")
}

// snapshotHandlerFixture builds a project directory with a devm.yaml
// that declares two snapshot entries, their hooks in devm.sh, and
// wires the project's MacCwd into a cache so handlers can resolve it.
// identity.Config points at a per-test $HOME so EntryDir() lands under
// t.TempDir(). Returns the identity + cache + project name + MacCwd +
// the mux the handler tests hit.
type snapshotFixture struct {
	t         *testing.T
	cfg       identity.Config
	cache     *StateCache
	project   string
	macCwd    string
	mux       *http.ServeMux
	runner    *fakeHookRunner
	entryDirA string
	entryDirB string
}

func newSnapshotFixture(t *testing.T, entries ...string) *snapshotFixture {
	t.Helper()
	if len(entries) == 0 {
		entries = []string{"pg", "redis"}
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := identity.Config{Name: "devm-test"}
	cache := NewStateCache()

	project := "demo"
	macCwd := filepath.Join(t.TempDir(), "proj")
	require.NoError(t, os.MkdirAll(macCwd, 0o755))

	// devm.sh declares the hook functions. Each entry needs
	// backup_<name> and restore_<name> or Validate rejects the config.
	var sh bytes.Buffer
	for _, e := range entries {
		sh.WriteString("backup_" + e + "() { echo ok; }\n")
		sh.WriteString("restore_" + e + "() { cat > /dev/null; }\n")
	}
	require.NoError(t, os.WriteFile(filepath.Join(macCwd, "devm.sh"), sh.Bytes(), 0o644))

	// devm.yaml declares the entries. Use on_stop: false so no extra
	// knobs collide with Validate, and snapshots are ad-hoc only.
	var ym bytes.Buffer
	ym.WriteString("project:\n  name: " + project + "\n")
	ym.WriteString("snapshots:\n")
	for _, e := range entries {
		ym.WriteString("  " + e + ":\n    on_stop: false\n")
	}
	require.NoError(t, os.WriteFile(filepath.Join(macCwd, "devm.yaml"), ym.Bytes(), 0o644))

	cache.SetMacCwd(project, macCwd)

	runner := &fakeHookRunner{
		backupPayload: []byte("hello"),
		restoreSink:   &bytes.Buffer{},
	}
	prev := newHookRunnerFn
	newHookRunnerFn = func(_ *tart.Tart, _ string) snapshot.HookRunner { return runner }
	t.Cleanup(func() { newHookRunnerFn = prev })

	mux := http.NewServeMux()
	// perProjectName empty → handlers read project from body/query,
	// the Mac-side daemon-socket shape.
	registerSnapshotRoutes(mux, cfg, cache, nil, "")

	entryDirA := snapshot.EntryDir(cfg, project, entries[0])
	entryDirB := ""
	if len(entries) >= 2 {
		entryDirB = snapshot.EntryDir(cfg, project, entries[1])
	}

	return &snapshotFixture{
		t:         t,
		cfg:       cfg,
		cache:     cache,
		project:   project,
		macCwd:    macCwd,
		mux:       mux,
		runner:    runner,
		entryDirA: entryDirA,
		entryDirB: entryDirB,
	}
}

// doJSON POSTs or GETs the handler and returns the recorder.
func (f *snapshotFixture) doJSON(method, target string, body any) *httptest.ResponseRecorder {
	f.t.Helper()
	var r *http.Request
	if body == nil {
		r = httptest.NewRequest(method, target, nil)
	} else {
		b, err := json.Marshal(body)
		require.NoError(f.t, err)
		r = httptest.NewRequest(method, target, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	f.mux.ServeHTTP(rr, r)
	return rr
}

func TestSnapshotHandler_Create_HappyPath(t *testing.T) {
	f := newSnapshotFixture(t)
	rr := f.doJSON(http.MethodPost, "/snapshot/create", map[string]any{
		"project": f.project, "entry": "pg", "source": "cli",
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Header().Get("Content-Type"), "application/json")

	var got snapshot.Meta
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
	assert.Equal(t, snapshot.SourceCLI, got.Source)
	assert.NotEmpty(t, got.TS)
	assert.Equal(t, "backup_pg", got.HookName)
	assert.Equal(t, "pg", f.runner.lastEntry)

	// Archive on disk.
	_, err := os.Stat(filepath.Join(f.entryDirA, got.TS+".zst"))
	require.NoError(t, err)
}

func TestSnapshotHandler_Create_DefaultsTimeoutTo10Min(t *testing.T) {
	f := newSnapshotFixture(t)

	// Fixture's devm.yaml declares `pg` with no `timeout:` set, so the
	// handler must fall back to the 10-minute default documented in
	// internal/skills/schema.md's `snapshots` section.
	var seen snapshot.CaptureConfig
	prev := captureFn
	captureFn = func(_ context.Context, cc snapshot.CaptureConfig) (snapshot.Meta, error) {
		seen = cc
		return snapshot.Meta{
			TS:          "20260316T120000Z",
			HookName:    "backup_" + cc.EntryName,
			ArchiveName: cc.EntryName,
			Source:      cc.Source,
		}, nil
	}
	t.Cleanup(func() { captureFn = prev })

	rr := f.doJSON(http.MethodPost, "/snapshot/create", map[string]any{
		"project": f.project, "entry": "pg", "source": "cli",
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal(t, 10*time.Minute, seen.StepTimeout)
	assert.Equal(t, 30*time.Second, seen.PreTimeout)
	assert.Equal(t, 30*time.Second, seen.PostTimeout)
}

func TestSnapshotHandler_Create_HonorsEntryTimeout(t *testing.T) {
	// Separate fixture that declares an explicit `timeout:` so the
	// resolver's entry branch is exercised alongside the default
	// branch covered by the test above.
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := identity.Config{Name: "devm-test"}
	cache := NewStateCache()
	project := "demo"
	macCwd := filepath.Join(t.TempDir(), "proj")
	require.NoError(t, os.MkdirAll(macCwd, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(macCwd, "devm.sh"),
		[]byte("backup_pg() { echo ok; }\nrestore_pg() { cat > /dev/null; }\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(macCwd, "devm.yaml"),
		[]byte("project:\n  name: "+project+"\nsnapshots:\n  pg:\n    timeout:\n      step: 45s\n"), 0o644))
	cache.SetMacCwd(project, macCwd)

	runner := &fakeHookRunner{backupPayload: []byte("hi"), restoreSink: &bytes.Buffer{}}
	prevHR := newHookRunnerFn
	newHookRunnerFn = func(_ *tart.Tart, _ string) snapshot.HookRunner { return runner }
	t.Cleanup(func() { newHookRunnerFn = prevHR })

	var seen snapshot.CaptureConfig
	prev := captureFn
	captureFn = func(_ context.Context, cc snapshot.CaptureConfig) (snapshot.Meta, error) {
		seen = cc
		return snapshot.Meta{TS: "20260316T120000Z", HookName: "backup_" + cc.EntryName}, nil
	}
	t.Cleanup(func() { captureFn = prev })

	mux := http.NewServeMux()
	registerSnapshotRoutes(mux, cfg, cache, nil, "")
	req := httptest.NewRequest(http.MethodPost, "/snapshot/create",
		strings.NewReader(`{"project":"demo","entry":"pg","source":"cli"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal(t, 45*time.Second, seen.StepTimeout)
}

func TestSnapshotHandler_Create_RejectsUnknownEntry(t *testing.T) {
	f := newSnapshotFixture(t)
	rr := f.doJSON(http.MethodPost, "/snapshot/create", map[string]any{
		"project": f.project, "entry": "nope", "source": "cli",
	})
	assert.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Body.String(), "nope")
}

func TestSnapshotHandler_Create_RejectsUnknownProject(t *testing.T) {
	f := newSnapshotFixture(t)
	rr := f.doJSON(http.MethodPost, "/snapshot/create", map[string]any{
		"project": "ghost", "entry": "pg", "source": "cli",
	})
	assert.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())
}

func TestSnapshotHandler_Create_RejectsUnknownSource(t *testing.T) {
	f := newSnapshotFixture(t)
	rr := f.doJSON(http.MethodPost, "/snapshot/create", map[string]any{
		"project": f.project, "entry": "pg", "source": "ad-hoc",
	})
	assert.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
}

func TestSnapshotHandler_Restore_DefaultsToLatest(t *testing.T) {
	f := newSnapshotFixture(t)
	// Seed one archive via Create.
	rr := f.doJSON(http.MethodPost, "/snapshot/create", map[string]any{
		"project": f.project, "entry": "pg", "source": "cli",
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	// ts omitted → handler resolves "latest" and feeds the archive to
	// the restore hook.
	rr = f.doJSON(http.MethodPost, "/snapshot/restore", map[string]any{
		"project": f.project, "entry": "pg",
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal(t, "pg", f.runner.lastEntry)
	// The seeded backup payload is what should have reached the
	// restore hook's stdin (zstd-encoded and decoded back).
	assert.Equal(t, "hello", f.runner.restoreSink.String())
	// Response names what happened so install hooks can distinguish
	// "actually restored" from "nothing to restore."
	var body restoreResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	assert.True(t, body.Restored)
}

// TestSnapshotHandler_Restore_NothingToRestore pins the first-boot
// case the install-hook pattern relies on: no archive exists yet, so
// the handler returns 200 with {"restored": false} instead of 500.
func TestSnapshotHandler_Restore_NothingToRestore(t *testing.T) {
	f := newSnapshotFixture(t)
	rr := f.doJSON(http.MethodPost, "/snapshot/restore", map[string]any{
		"project": f.project, "entry": "pg",
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var body restoreResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	assert.False(t, body.Restored, "with no archive on disk restore must report false")
	assert.Empty(t, f.runner.restoreSink.String(), "the restore hook must not have been called")
}

func TestSnapshotHandler_List_AllEntries(t *testing.T) {
	f := newSnapshotFixture(t, "pg", "redis")
	// One archive per declared entry.
	for _, e := range []string{"pg", "redis"} {
		rr := f.doJSON(http.MethodPost, "/snapshot/create", map[string]any{
			"project": f.project, "entry": e, "source": "cli",
		})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	}
	// entry omitted → list all declared entries' latest.
	rr := f.doJSON(http.MethodGet, "/snapshot/list?project="+f.project, nil)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var metas []snapshot.Meta
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &metas))
	require.Len(t, metas, 2)
	// Both declared entries present (order is newest first, order
	// across entries is implementation-defined but each must appear).
	hooks := map[string]bool{}
	for _, m := range metas {
		hooks[m.HookName] = true
	}
	assert.True(t, hooks["backup_pg"])
	assert.True(t, hooks["backup_redis"])
}

func TestSnapshotHandler_List_FlagsOrphansForDirsNotInYaml(t *testing.T) {
	// Fixture declares "pg" and "redis"; seed an archive under each,
	// then seed one more under "ghost" — a dir on disk whose entry
	// name is NOT in the current yaml. The no-entry listing must
	// return a synthetic row for "ghost" with Orphan=true.
	f := newSnapshotFixture(t, "pg", "redis")
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	seedArchive(t, f.entryDirA, snapshot.NewTimestamp(base), "backup_pg")
	seedArchive(t, f.entryDirB, snapshot.NewTimestamp(base.Add(time.Second)), "backup_redis")
	ghostDir := snapshot.EntryDir(f.cfg, f.project, "ghost")
	ghostTS := snapshot.NewTimestamp(base.Add(2 * time.Second))
	seedArchive(t, ghostDir, ghostTS, "backup_ghost")

	rr := f.doJSON(http.MethodGet, "/snapshot/list?project="+f.project, nil)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var metas []snapshot.Meta
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &metas))

	var orphans []snapshot.Meta
	known := map[string]snapshot.Meta{}
	for _, m := range metas {
		if m.Orphan {
			orphans = append(orphans, m)
			continue
		}
		known[m.HookName] = m
	}
	assert.True(t, known["backup_pg"].TS != "", "yaml-declared pg must appear")
	assert.True(t, known["backup_redis"].TS != "", "yaml-declared redis must appear")
	require.Len(t, orphans, 1, "ghost dir not in yaml must appear as orphan")
	assert.Equal(t, "ghost", orphans[0].ArchiveName)
	assert.Equal(t, ghostTS, orphans[0].TS, "orphan row carries its latest archive's ts")
}

func TestSnapshotHandler_List_SingleEntry(t *testing.T) {
	f := newSnapshotFixture(t)
	// Seed two archives with distinct timestamps (Capture granularity
	// is one second; the test sidesteps the sleep by writing the
	// on-disk shape directly).
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	seedArchive(t, f.entryDirA, snapshot.NewTimestamp(base), "backup_pg")
	seedArchive(t, f.entryDirA, snapshot.NewTimestamp(base.Add(5*time.Second)), "backup_pg")

	rr := f.doJSON(http.MethodGet, "/snapshot/list?project="+f.project+"&entry=pg", nil)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var metas []snapshot.Meta
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &metas))
	// Both archives returned, newest first (strings descending).
	require.Len(t, metas, 2)
	assert.True(t, metas[0].TS >= metas[1].TS, "newest first; got %q then %q", metas[0].TS, metas[1].TS)
}

func TestSnapshotHandler_Prune_RespectsKeep(t *testing.T) {
	f := newSnapshotFixture(t)
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		seedArchive(t, f.entryDirA, snapshot.NewTimestamp(base.Add(time.Duration(i)*time.Second)), "backup_pg")
	}
	rr := f.doJSON(http.MethodPost, "/snapshot/prune", map[string]any{
		"project": f.project, "entry": "pg", "keep": 1,
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var resp struct {
		Removed []string `json:"removed"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Len(t, resp.Removed, 2)
	// Only one archive left on disk.
	entries, err := os.ReadDir(f.entryDirA)
	require.NoError(t, err)
	count := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".zst") {
			count++
		}
	}
	assert.Equal(t, 1, count)
}

func TestSnapshotHandler_Show_HappyPath(t *testing.T) {
	f := newSnapshotFixture(t)
	rr := f.doJSON(http.MethodPost, "/snapshot/create", map[string]any{
		"project": f.project, "entry": "pg", "source": "cli",
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var created snapshot.Meta
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))

	// ts omitted → latest.
	rr = f.doJSON(http.MethodGet, "/snapshot/show?project="+f.project+"&entry=pg", nil)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var got snapshot.Meta
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
	assert.Equal(t, created, got)

	// Explicit ts.
	rr = f.doJSON(http.MethodGet, "/snapshot/show?project="+f.project+"&entry=pg&ts="+created.TS, nil)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
	assert.Equal(t, created, got)
}

func TestSnapshotHandler_MethodNotAllowed(t *testing.T) {
	f := newSnapshotFixture(t)
	// POST on a GET endpoint.
	rr := f.doJSON(http.MethodPost, "/snapshot/list?project="+f.project, nil)
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	// GET on a POST endpoint.
	rr = f.doJSON(http.MethodGet, "/snapshot/create", nil)
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestSnapshotHandler_Clear_HappyPath(t *testing.T) {
	f := newSnapshotFixture(t)
	// Seed one archive via Create so the dir exists and has files.
	rr := f.doJSON(http.MethodPost, "/snapshot/create", map[string]any{
		"project": f.project, "entry": "pg", "source": "cli",
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	rr = f.doJSON(http.MethodPost, "/snapshot/clear", map[string]any{
		"project": f.project, "entry": "pg",
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var body clearResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal(t, 1, body.Removed)

	// The entry dir is gone so DiscoverOrphans does not pick it up
	// after a clear of a yaml-undeclared entry.
	_, err := os.Stat(f.entryDirA)
	assert.ErrorIs(t, err, fs.ErrNotExist)
}

func TestSnapshotHandler_Clear_AcceptsOrphanEntry(t *testing.T) {
	// The clear endpoint skips requireEntry so an operator can clean up
	// an entry that's been removed from devm.yaml. Pin that: an entry
	// name the yaml does NOT declare still succeeds, and its on-disk
	// dir is emptied if present.
	f := newSnapshotFixture(t)
	orphanDir := snapshot.EntryDir(f.cfg, f.project, "orphan")
	require.NoError(t, os.MkdirAll(orphanDir, 0o755))
	ts := "2026-11-17T1000-00"
	require.NoError(t, os.WriteFile(filepath.Join(orphanDir, ts+".zst"), []byte("x"), 0o644))
	require.NoError(t, snapshot.WriteMeta(orphanDir, ts, snapshot.Meta{TS: ts}))

	rr := f.doJSON(http.MethodPost, "/snapshot/clear", map[string]any{
		"project": f.project, "entry": "orphan",
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var body clearResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal(t, 1, body.Removed)
}

func TestSnapshotHandler_Clear_RejectsEmptyEntry(t *testing.T) {
	f := newSnapshotFixture(t)
	rr := f.doJSON(http.MethodPost, "/snapshot/clear", map[string]any{
		"project": f.project,
	})
	assert.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
}

func TestSnapshotHandler_Clear_RejectsUnknownProject(t *testing.T) {
	f := newSnapshotFixture(t)
	rr := f.doJSON(http.MethodPost, "/snapshot/clear", map[string]any{
		"project": "ghost", "entry": "pg",
	})
	assert.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())
}

func TestSnapshotHandler_Clear_MissingDirReturnsZero(t *testing.T) {
	f := newSnapshotFixture(t)
	rr := f.doJSON(http.MethodPost, "/snapshot/clear", map[string]any{
		"project": f.project, "entry": "pg",
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var body clearResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal(t, 0, body.Removed)
}

// TestSnapshotHandler_Create_PathMode_WiresPathAndTarRunner pins the
// path-mode wiring the handler adds at Task 11: when an entry declares
// `path:`, CaptureConfig.Path and CaptureConfig.TarRunner are both
// non-nil so Capture takes the path-mode branch.
func TestSnapshotHandler_Create_PathMode_WiresPathAndTarRunner(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfgID := identity.Config{Name: "devm-test"}
	cache := NewStateCache()
	project := "demo"
	macCwd := filepath.Join(t.TempDir(), "proj")
	require.NoError(t, os.MkdirAll(macCwd, 0o755))
	// No devm.sh backup_<name> required — path mode skips the hook.
	require.NoError(t, os.WriteFile(filepath.Join(macCwd, "devm.yaml"),
		[]byte("project:\n  name: "+project+"\nsnapshots:\n  pg:\n    path: /var/lib/postgresql\n"), 0o644))
	cache.SetMacCwd(project, macCwd)

	prevHR := newHookRunnerFn
	newHookRunnerFn = func(_ *tart.Tart, _ string) snapshot.HookRunner { return &fakeHookRunner{} }
	t.Cleanup(func() { newHookRunnerFn = prevHR })

	tarStub := &stubTarRunner{}
	prevTR := newTarRunnerFn
	newTarRunnerFn = func(_ *tart.Tart, _ string) snapshot.TarRunner { return tarStub }
	t.Cleanup(func() { newTarRunnerFn = prevTR })

	var seen snapshot.CaptureConfig
	prev := captureFn
	captureFn = func(_ context.Context, cc snapshot.CaptureConfig) (snapshot.Meta, error) {
		seen = cc
		return snapshot.Meta{TS: "2026-11-16T0300-00"}, nil
	}
	t.Cleanup(func() { captureFn = prev })

	mux := http.NewServeMux()
	registerSnapshotRoutes(mux, cfgID, cache, nil, "")
	req := httptest.NewRequest(http.MethodPost, "/snapshot/create",
		strings.NewReader(`{"project":"demo","entry":"pg","source":"cli"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	require.NotNil(t, seen.Path, "yaml declared path: must flow to CaptureConfig.Path")
	assert.Equal(t, "/var/lib/postgresql", *seen.Path)
	assert.NotNil(t, seen.TarRunner, "CaptureConfig.TarRunner must be wired for path mode")
}

// TestSnapshotHandler_Restore_PathMode_WiresPathAndForce pins the
// path-mode restore wiring: Path, TarRunner, and the Force flag from
// the request body all reach RestoreConfig.
func TestSnapshotHandler_Restore_PathMode_WiresPathAndForce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfgID := identity.Config{Name: "devm-test"}
	cache := NewStateCache()
	project := "demo"
	macCwd := filepath.Join(t.TempDir(), "proj")
	require.NoError(t, os.MkdirAll(macCwd, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(macCwd, "devm.yaml"),
		[]byte("project:\n  name: "+project+"\nsnapshots:\n  pg:\n    path: /var/lib/postgresql\n"), 0o644))
	cache.SetMacCwd(project, macCwd)

	// Seed an archive on disk so Restore finds something.
	dir := snapshot.EntryDir(cfgID, project, "pg")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	ts := "2026-11-17T1000-00"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ts+".zst"), []byte("x"), 0o644))
	require.NoError(t, snapshot.WriteMeta(dir, ts, snapshot.Meta{
		TS: ts, Mode: snapshot.ModePath, CapturedPath: "/var/lib/postgresql", IsDir: true, Status: snapshot.StatusOk,
	}))
	require.NoError(t, snapshot.UpdateLatest(dir, ts))

	stubHook := &pathRestoreHookStub{}
	prevHR := newHookRunnerFn
	newHookRunnerFn = func(_ *tart.Tart, _ string) snapshot.HookRunner { return stubHook }
	t.Cleanup(func() { newHookRunnerFn = prevHR })

	stubTar := &stubTarRunner{}
	prevTR := newTarRunnerFn
	newTarRunnerFn = func(_ *tart.Tart, _ string) snapshot.TarRunner { return stubTar }
	t.Cleanup(func() { newTarRunnerFn = prevTR })

	mux := http.NewServeMux()
	registerSnapshotRoutes(mux, cfgID, cache, nil, "")
	req := httptest.NewRequest(http.MethodPost, "/snapshot/restore",
		strings.NewReader(`{"project":"demo","entry":"pg","force":true}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	// TarRunner saw path mode (RunRestoreTar was invoked with the
	// declared path).
	assert.Equal(t, "/var/lib/postgresql", stubTar.lastRestorePath)
	assert.True(t, stubTar.lastRestoreIsDir)
}

// stubTarRunner is a no-op snapshot.TarRunner the handler tests inject
// via newTarRunnerFn when a capture/restore reaches the path-mode
// branch. Fields record the last call.
type stubTarRunner struct {
	lastBackupSource string
	lastBackupBase   string
	lastBackupIsDir  bool
	lastRestorePath  string
	lastRestoreIsDir bool
}

func (s *stubTarRunner) RunBackupTar(_ context.Context, _ /*entryName*/, source, declaredBasename string, isDir bool, w io.Writer) (int, error) {
	s.lastBackupSource = source
	s.lastBackupBase = declaredBasename
	s.lastBackupIsDir = isDir
	// Emit a byte so downstream zstd writer has something to close
	// cleanly; a true no-op is also fine but keeping it symmetric with
	// the backup side is clearer.
	_, _ = w.Write([]byte{0})
	return 0, nil
}
func (s *stubTarRunner) RunRestoreTar(_ context.Context, _ /*entryName*/, declaredPath string, isDir bool, stdin io.Reader) (int, error) {
	s.lastRestorePath = declaredPath
	s.lastRestoreIsDir = isDir
	// Drain stdin to prevent the zstd pipeline above from stalling.
	_, _ = io.Copy(io.Discard, stdin)
	return 0, nil
}

// StatPath reports the handler-test path as an existing directory with
// no symlink resolution: handler tests don't care about stat details,
// just that the handler wires TarRunner through to Capture.
func (s *stubTarRunner) StatPath(_ context.Context, _ /*entryName*/, guestPath string) (bool, bool, string, error) {
	return true, true, guestPath, nil
}

// pathRestoreHookStub implements snapshot.HookRunner for path-mode
// restore tests. HookExists reports no hooks (so no pre/post fire); the
// four stream-mode methods aren't reached on this code path.
type pathRestoreHookStub struct{}

func (pathRestoreHookStub) RunBackup(context.Context, string, io.Writer) (int, error) {
	panic("not used")
}
func (pathRestoreHookStub) RunRestore(context.Context, string, io.Reader) (int, error) {
	panic("not used")
}
func (pathRestoreHookStub) RunPreBackup(context.Context, string, map[string]string) (string, int, error) {
	return "", 0, nil
}
func (pathRestoreHookStub) RunPostBackup(context.Context, string, map[string]string) (int, error) {
	return 0, nil
}
func (pathRestoreHookStub) RunPreRestore(context.Context, string, map[string]string) (int, error) {
	return 0, nil
}
func (pathRestoreHookStub) RunPostRestore(context.Context, string, map[string]string) (int, error) {
	return 0, nil
}
func (pathRestoreHookStub) HookExists(context.Context, string) (snapshot.HookPresence, error) {
	return snapshot.HookPresence{}, nil
}

func TestSnapshotHandler_PerProjectPin_IgnoresBodyProject(t *testing.T) {
	// Simulate the guest-API listener shape: perProjectName pins the
	// handler to one project and ignores "project" in the body.
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := identity.Config{Name: "devm-test"}
	cache := NewStateCache()
	project := "pinned"
	macCwd := filepath.Join(t.TempDir(), "proj")
	require.NoError(t, os.MkdirAll(macCwd, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(macCwd, "devm.sh"),
		[]byte("backup_pg() { echo ok; }\nrestore_pg() { cat > /dev/null; }\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(macCwd, "devm.yaml"),
		[]byte("project:\n  name: "+project+"\nsnapshots:\n  pg:\n    on_stop: false\n"), 0o644))
	cache.SetMacCwd(project, macCwd)

	runner := &fakeHookRunner{backupPayload: []byte("hi"), restoreSink: &bytes.Buffer{}}
	prev := newHookRunnerFn
	newHookRunnerFn = func(_ *tart.Tart, _ string) snapshot.HookRunner { return runner }
	t.Cleanup(func() { newHookRunnerFn = prev })

	mux := http.NewServeMux()
	registerSnapshotRoutes(mux, cfg, cache, nil, project)

	// Body names "someone-else", handler must use the pinned project.
	req := httptest.NewRequest(http.MethodPost, "/snapshot/create",
		strings.NewReader(`{"project":"someone-else","entry":"pg","source":"cli"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	// Archive landed under the PINNED project, not "someone-else".
	dir := snapshot.EntryDir(cfg, project, "pg")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	count := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".zst") {
			count++
		}
	}
	assert.Equal(t, 1, count)
}

// TestSnapshotHandler_Restore_DefaultsTimeouts pins I-3: the restore
// handler must thread per-phase timeouts into RestoreConfig. An unset
// yaml `timeout:` block falls back to schema defaults (same shape the
// create handler already applies).
func TestSnapshotHandler_Restore_DefaultsTimeouts(t *testing.T) {
	f := newSnapshotFixture(t)

	var seen snapshot.RestoreConfig
	prev := restoreFn
	restoreFn = func(_ context.Context, rc snapshot.RestoreConfig) (bool, error) {
		seen = rc
		return true, nil
	}
	t.Cleanup(func() { restoreFn = prev })

	rr := f.doJSON(http.MethodPost, "/snapshot/restore", map[string]any{
		"project": f.project, "entry": "pg",
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal(t, 30*time.Second, seen.PreTimeout, "pre falls back to schema default")
	assert.Equal(t, 10*time.Minute, seen.StepTimeout, "step falls back to schema default")
	assert.Equal(t, 30*time.Second, seen.PostTimeout, "post falls back to schema default")
}

// TestSnapshotHandler_Restore_HonorsEntryTimeouts pins the yaml-honoring
// side of I-3: an entry that sets `timeout.step:` etc flows into the
// RestoreConfig the handler hands to snapshot.Restore.
func TestSnapshotHandler_Restore_HonorsEntryTimeouts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfgID := identity.Config{Name: "devm-test"}
	cache := NewStateCache()
	project := "demo"
	macCwd := filepath.Join(t.TempDir(), "proj")
	require.NoError(t, os.MkdirAll(macCwd, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(macCwd, "devm.sh"),
		[]byte("backup_pg() { echo ok; }\nrestore_pg() { cat > /dev/null; }\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(macCwd, "devm.yaml"),
		[]byte("project:\n  name: "+project+"\nsnapshots:\n  pg:\n    timeout:\n      pre: 7s\n      step: 1m\n      post: 15s\n"), 0o644))
	cache.SetMacCwd(project, macCwd)

	var seen snapshot.RestoreConfig
	prev := restoreFn
	restoreFn = func(_ context.Context, rc snapshot.RestoreConfig) (bool, error) {
		seen = rc
		return true, nil
	}
	t.Cleanup(func() { restoreFn = prev })

	mux := http.NewServeMux()
	registerSnapshotRoutes(mux, cfgID, cache, nil, "")
	req := httptest.NewRequest(http.MethodPost, "/snapshot/restore",
		strings.NewReader(`{"project":"demo","entry":"pg"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	assert.Equal(t, 7*time.Second, seen.PreTimeout)
	assert.Equal(t, time.Minute, seen.StepTimeout)
	assert.Equal(t, 15*time.Second, seen.PostTimeout)
}
