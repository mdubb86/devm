package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// overrideEndpoint swaps *v to val for the duration of the test. The
// snapshot endpoints are package-level vars precisely so tests can
// aim them at a fake HTTP server.
func overrideEndpoint(t *testing.T, v *string, val string) {
	t.Helper()
	orig := *v
	*v = val
	t.Cleanup(func() { *v = orig })
}

func TestSnapshotCreate_MainPostsToGuestAPI(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody struct {
		Entry  string `json:"entry"`
		Source string `json:"source"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ts":"2026-11-16T0300-00","hook_name":"backup_pg","archive_name":"2026-11-16T0300-00.zst","source":"cli","exit_code":0,"duration_ms":1500,"size_bytes":2048}`)
	}))
	defer srv.Close()
	overrideEndpoint(t, &snapshotCreateEndpoint, srv.URL+"/snapshot/create")

	stdout := captureStdout(t, func() {
		code := snapshotMain([]string{"create", "pg"})
		assert.Equal(t, 0, code)
	})

	assert.Equal(t, "/snapshot/create", gotPath)
	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, "pg", gotBody.Entry)
	assert.Equal(t, "cli", gotBody.Source, "default source for `gdevm snapshot create` is an operator-driven cli call")
	assert.Contains(t, stdout, "snapshot pg saved")
	assert.Contains(t, stdout, "2026-11-16T0300-00")
	assert.Contains(t, stdout, "2.0 KiB")
}

// TestSnapshotCreate_SourceCronFromTimer pins that the in-guest timer's
// `--source cron` reaches the daemon unchanged. RenderSnapshotService
// writes this flag into the systemd unit ExecStart.
func TestSnapshotCreate_SourceCronFromTimer(t *testing.T) {
	var gotBody struct {
		Entry  string `json:"entry"`
		Source string `json:"source"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ts":"2026-11-16T0300-00","source":"cron","size_bytes":2048}`)
	}))
	defer srv.Close()
	overrideEndpoint(t, &snapshotCreateEndpoint, srv.URL+"/snapshot/create")

	captureStdout(t, func() {
		code := snapshotMain([]string{"create", "pg", "--source", "cron"})
		assert.Equal(t, 0, code)
	})
	assert.Equal(t, "pg", gotBody.Entry)
	assert.Equal(t, "cron", gotBody.Source)
}

// TestSnapshotCreate_RejectsUnknownSource pins that only "cli" and
// "cron" are honored — any other value fails before the HTTP call.
func TestSnapshotCreate_RejectsUnknownSource(t *testing.T) {
	stderr := captureStderr(t, func() {
		code := snapshotMain([]string{"create", "pg", "--source", "other"})
		assert.Equal(t, 2, code)
	})
	assert.Contains(t, stderr, "--source")
	assert.Contains(t, stderr, "other")
}

func TestSnapshotList_MainReturnsMetas(t *testing.T) {
	var gotPath, gotEntry string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotEntry = r.URL.Query().Get("entry")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[
			{"ts":"2026-11-16T0300-00","source":"cron","size_bytes":2048},
			{"ts":"2026-11-15T0300-00","source":"cli","size_bytes":4096}
		]`)
	}))
	defer srv.Close()
	overrideEndpoint(t, &snapshotListEndpoint, srv.URL+"/snapshot/list")

	stdout := captureStdout(t, func() {
		code := snapshotMain([]string{"list", "pg"})
		assert.Equal(t, 0, code)
	})

	assert.Equal(t, "/snapshot/list", gotPath)
	assert.Equal(t, "pg", gotEntry)
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	require.Len(t, lines, 2, "want two lines, got %q", stdout)
	assert.Contains(t, lines[0], "2026-11-16T0300-00")
	assert.Contains(t, lines[0], "cron")
	assert.Contains(t, lines[1], "2026-11-15T0300-00")
	assert.Contains(t, lines[1], "cli")
}

func TestSnapshotList_MainOmitsEntryWhenAbsent(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[]`)
	}))
	defer srv.Close()
	overrideEndpoint(t, &snapshotListEndpoint, srv.URL+"/snapshot/list")

	captureStdout(t, func() {
		code := snapshotMain([]string{"list"})
		assert.Equal(t, 0, code)
	})
	assert.Equal(t, "", gotQuery, "no args should mean no entry= in query")
}

// TestSnapshotList_NoEntryTabulatesNewColumns pins the observability
// close: when `gdevm snapshot list` runs without an entry argument,
// output must carry the NAME / MODE / STATUS / LATEST / SIZE / SOURCE
// columns the Mac CLI prints, so operators can see mode and status
// drift from inside the guest without round-tripping to the Mac.
// A v0.31 sidecar's empty mode must render as "stream" and empty
// status as "ok".
func TestSnapshotList_NoEntryTabulatesNewColumns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[
			{"ts":"2026-11-16T0300-00","archive_name":"claude","source":"on_teardown","size_bytes":2048,"mode":"path","status":"ok"},
			{"ts":"2026-11-15T0300-00","archive_name":"pg","source":"cli","size_bytes":4096}
		]`)
	}))
	defer srv.Close()
	overrideEndpoint(t, &snapshotListEndpoint, srv.URL+"/snapshot/list")

	stdout := captureStdout(t, func() {
		code := snapshotMain([]string{"list"})
		assert.Equal(t, 0, code)
	})
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	require.GreaterOrEqual(t, len(lines), 3, "want header plus one row per entry: got %q", stdout)
	assert.Contains(t, lines[0], "NAME")
	assert.Contains(t, lines[0], "MODE")
	assert.Contains(t, lines[0], "STATUS")
	assert.Contains(t, lines[0], "LATEST")
	assert.NotContains(t, lines[0], "ORPHAN", "no orphan rows means no ORPHAN column")
	assert.Contains(t, stdout, "claude")
	assert.Contains(t, stdout, "path")
	// v0.31 sidecar (empty mode) renders as "stream"; empty status as "ok".
	assert.Regexp(t, `pg\s+stream\s+ok`, stdout)
}

// TestSnapshotList_OrphanShowsOrphanColumn pins the complement: the
// ORPHAN column only appears when at least one row needs it, and
// marks the orphan rows explicitly.
func TestSnapshotList_OrphanShowsOrphanColumn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[
			{"ts":"2026-11-16T0300-00","archive_name":"claude","source":"on_teardown","size_bytes":2048,"mode":"path"},
			{"ts":"2026-11-10T0300-00","archive_name":"ghost","source":"cli","size_bytes":512,"orphan":true}
		]`)
	}))
	defer srv.Close()
	overrideEndpoint(t, &snapshotListEndpoint, srv.URL+"/snapshot/list")

	stdout := captureStdout(t, func() {
		code := snapshotMain([]string{"list"})
		assert.Equal(t, 0, code)
	})
	assert.Contains(t, stdout, "ORPHAN")
	assert.Contains(t, stdout, "orphan")
}

func TestSnapshotRestore_DefaultsToLatest(t *testing.T) {
	var gotBody struct {
		Entry string `json:"entry"`
		TS    string `json:"ts"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"restored":true}`)
	}))
	defer srv.Close()
	overrideEndpoint(t, &snapshotRestoreEndpoint, srv.URL+"/snapshot/restore")

	stdout := captureStdout(t, func() {
		code := snapshotMain([]string{"restore", "pg"})
		assert.Equal(t, 0, code)
	})
	assert.Equal(t, "pg", gotBody.Entry)
	assert.Equal(t, "", gotBody.TS)
	assert.Contains(t, stdout, "restored pg from latest")
}

// TestSnapshotRestore_NothingToRestore pins the first-boot case: the
// daemon reports there was nothing to restore. gdevm exits 0 and
// prints a stderr diagnostic so install hooks see a clean no-op.
func TestSnapshotRestore_NothingToRestore(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"restored":false}`)
	}))
	defer srv.Close()
	overrideEndpoint(t, &snapshotRestoreEndpoint, srv.URL+"/snapshot/restore")

	var code int
	stderr := captureStderr(t, func() {
		stdout := captureStdout(t, func() {
			code = snapshotMain([]string{"restore", "pg"})
		})
		assert.Empty(t, stdout, "stdout stays empty — no restore happened")
	})
	assert.Equal(t, 0, code, "no archive to restore is not an error")
	assert.Contains(t, stderr, "no snapshot to restore for pg")
}

func TestSnapshotRestore_ExplicitTS(t *testing.T) {
	var gotBody struct {
		Entry string `json:"entry"`
		TS    string `json:"ts"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"restored":true}`)
	}))
	defer srv.Close()
	overrideEndpoint(t, &snapshotRestoreEndpoint, srv.URL+"/snapshot/restore")

	stdout := captureStdout(t, func() {
		code := snapshotMain([]string{"restore", "pg", "2026-01-02T0100-00"})
		assert.Equal(t, 0, code)
	})
	assert.Equal(t, "pg", gotBody.Entry)
	assert.Equal(t, "2026-01-02T0100-00", gotBody.TS)
	assert.Contains(t, stdout, "restored pg from 2026-01-02T0100-00")
}

func TestSnapshotPrune_RequiresKeep(t *testing.T) {
	stderr := captureStderr(t, func() {
		code := snapshotMain([]string{"prune", "pg"})
		assert.Equal(t, 2, code)
	})
	assert.Contains(t, stderr, "--keep")
}

func TestSnapshotPrune_PostsAndReportsCount(t *testing.T) {
	var gotPath string
	var gotBody struct {
		Entry string `json:"entry"`
		Keep  int    `json:"keep"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"removed":["2026-11-01T0300-00","2026-11-02T0300-00"]}`)
	}))
	defer srv.Close()
	overrideEndpoint(t, &snapshotPruneEndpoint, srv.URL+"/snapshot/prune")

	stdout := captureStdout(t, func() {
		code := snapshotMain([]string{"prune", "pg", "--keep", "3"})
		assert.Equal(t, 0, code)
	})
	assert.Equal(t, "/snapshot/prune", gotPath)
	assert.Equal(t, "pg", gotBody.Entry)
	assert.Equal(t, 3, gotBody.Keep)
	assert.Contains(t, stdout, "pruned 2 snapshot(s)")
}

func TestSnapshotShow_PrintsMetaLines(t *testing.T) {
	var gotEntry, gotTS string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		gotEntry = r.URL.Query().Get("entry")
		gotTS = r.URL.Query().Get("ts")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ts":"2026-11-16T0300-00","hook_name":"backup_pg","archive_name":"2026-11-16T0300-00.zst","source":"cron","exit_code":0,"duration_ms":1500,"size_bytes":2048}`)
	}))
	defer srv.Close()
	overrideEndpoint(t, &snapshotShowEndpoint, srv.URL+"/snapshot/show")

	stdout := captureStdout(t, func() {
		code := snapshotMain([]string{"show", "pg", "2026-11-16T0300-00"})
		assert.Equal(t, 0, code)
	})
	assert.Equal(t, "pg", gotEntry)
	assert.Equal(t, "2026-11-16T0300-00", gotTS)
	assert.Contains(t, stdout, "ts:            2026-11-16T0300-00")
	assert.Contains(t, stdout, "hook_name:     backup_pg")
	assert.Contains(t, stdout, "source:        cron")
	assert.Contains(t, stdout, "size:          2.0 KiB (2048 bytes)")
	// v0.31 sidecar: no mode / status set — show must render the
	// defaulted values so operators see the row in full.
	assert.Contains(t, stdout, "mode:          stream")
	assert.Contains(t, stdout, "status:        ok")
}

// TestSnapshotShow_PrintsPathModeFields pins the observability close
// on show: a path-mode archive must surface mode / status /
// captured_path / is_dir inline, so an in-guest operator sees the same
// detail the Mac CLI does.
func TestSnapshotShow_PrintsPathModeFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ts":"2026-11-16T0300-00","archive_name":"claude","source":"on_teardown","exit_code":0,"duration_ms":500,"size_bytes":2048,"mode":"path","status":"post_failed","captured_path":"/home/devm/.claude","is_dir":true}`)
	}))
	defer srv.Close()
	overrideEndpoint(t, &snapshotShowEndpoint, srv.URL+"/snapshot/show")

	stdout := captureStdout(t, func() {
		code := snapshotMain([]string{"show", "claude", "2026-11-16T0300-00"})
		assert.Equal(t, 0, code)
	})
	assert.Contains(t, stdout, "mode:          path")
	assert.Contains(t, stdout, "status:        post_failed")
	assert.Contains(t, stdout, "captured_path: /home/devm/.claude")
	assert.Contains(t, stdout, "is_dir:        true")
}

func TestSnapshot_UnknownSubverb(t *testing.T) {
	stderr := captureStderr(t, func() {
		code := snapshotMain([]string{"flarp"})
		assert.Equal(t, 2, code)
	})
	assert.Contains(t, stderr, "unknown subcommand")
}

func TestSnapshot_NoSubverb(t *testing.T) {
	stderr := captureStderr(t, func() {
		code := snapshotMain(nil)
		assert.Equal(t, 2, code)
	})
	assert.Contains(t, stderr, "usage:")
}

func TestSnapshotRestore_ForceFlagReachesWire(t *testing.T) {
	var gotBody struct {
		Entry string `json:"entry"`
		TS    string `json:"ts"`
		Force bool   `json:"force"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	overrideEndpoint(t, &snapshotRestoreEndpoint, srv.URL+"/snapshot/restore")

	captureStdout(t, func() {
		code := snapshotMain([]string{"restore", "pg", "--force"})
		assert.Equal(t, 0, code)
	})
	assert.Equal(t, "pg", gotBody.Entry)
	assert.True(t, gotBody.Force)
}

func TestSnapshotClear_PostsAndPrintsCount(t *testing.T) {
	var gotPath string
	var gotBody struct {
		Entry string `json:"entry"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"removed":3}`)
	}))
	defer srv.Close()
	overrideEndpoint(t, &snapshotClearEndpoint, srv.URL+"/snapshot/clear")

	stdout := captureStdout(t, func() {
		code := snapshotMain([]string{"clear", "pg"})
		assert.Equal(t, 0, code)
	})
	assert.Equal(t, "/snapshot/clear", gotPath)
	assert.Equal(t, "pg", gotBody.Entry)
	assert.Contains(t, stdout, "cleared 3 archive(s) for pg")
}

func TestSnapshotClear_RequiresName(t *testing.T) {
	stderr := captureStderr(t, func() {
		code := snapshotMain([]string{"clear"})
		assert.Equal(t, 2, code)
	})
	assert.Contains(t, stderr, "clear")
}

func TestSnapshotCreate_SurfacesDaemonError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"entry \"nope\" is not declared in devm.yaml snapshots:"}`)
	}))
	defer srv.Close()
	overrideEndpoint(t, &snapshotCreateEndpoint, srv.URL+"/snapshot/create")

	stderr := captureStderr(t, func() {
		code := snapshotMain([]string{"create", "nope"})
		assert.NotEqual(t, 0, code)
	})
	assert.Contains(t, stderr, "not declared")
}
