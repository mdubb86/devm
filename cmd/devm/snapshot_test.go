package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/mdubb86/devm/internal/snapshot"
)

// startFakeSnapshotDaemon stands up an http.Server on a unix socket and
// returns the socket path. The caller registers handlers on mux; the
// socket cleans itself up at test end.
//
// The directory is created under the default TMPDIR with a short prefix
// — darwin caps unix socket path length at ~104 bytes, so the longer
// t.TempDir() path (which embeds the test name) overflows for any test
// whose name is more than a handful of characters.
func startFakeSnapshotDaemon(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ds")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	srv := &http.Server{Handler: mux}
	done := make(chan struct{})
	go func() {
		_ = srv.Serve(ln)
		close(done)
	}()
	t.Cleanup(func() {
		_ = srv.Close()
		<-done
	})
	// Wait for the socket to appear so the client doesn't race the listener.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := net.Dial("unix", sock); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	return sock
}

func newClient(t *testing.T, mux *http.ServeMux) *serviceapi.Client {
	t.Helper()
	sock := startFakeSnapshotDaemon(t, mux)
	return serviceapi.NewClientWithSocket(sock)
}

func TestSnapshotCreate_Dispatches(t *testing.T) {
	var gotBody struct {
		Project string `json:"project"`
		Entry   string `json:"entry"`
		Source  string `json:"source"`
	}
	var gotMethod, gotPath string
	mux := http.NewServeMux()
	mux.HandleFunc("/snapshot/create", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snapshot.Meta{
			TS:          "2026-11-16T0300-00",
			HookName:    "backup_pg",
			ArchiveName: "2026-11-16T0300-00.zst",
			Source:      snapshot.SourceCLI,
			DurationMs:  2400,
			SizeBytes:   1258291, // ~1.2 MiB
		})
	})
	c := newClient(t, mux)

	var out bytes.Buffer
	err := runSnapshotCreate(context.Background(), c, "p", "pg", &out)
	require.NoError(t, err)

	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, "/snapshot/create", gotPath)
	assert.Equal(t, "p", gotBody.Project)
	assert.Equal(t, "pg", gotBody.Entry)
	assert.Equal(t, "cli", gotBody.Source)
	assert.Contains(t, out.String(), "pg")
	assert.Contains(t, out.String(), "2026-11-16T0300-00")
}

func TestSnapshotCreate_SurfacesDaemonError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/snapshot/create", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "unknown snapshot entry pg"})
	})
	c := newClient(t, mux)

	err := runSnapshotCreate(context.Background(), c, "p", "pg", io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown snapshot entry pg")
}

func TestSnapshotList_NoArg_TabulatesColumns(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/snapshot/list", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "p", r.URL.Query().Get("project"))
		require.Equal(t, "", r.URL.Query().Get("entry"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]snapshot.Meta{
			{TS: "2026-11-16T0300-00", HookName: "backup_pg", ArchiveName: "pg", Source: snapshot.SourceCron, DurationMs: 2400, SizeBytes: 1258291, Status: snapshot.StatusOk},
			{TS: "2026-11-15T0300-00", HookName: "backup_mysql", ArchiveName: "mysql", Source: snapshot.SourceCLI, DurationMs: 1800, SizeBytes: 524288, Status: snapshot.StatusOk},
		})
	})
	c := newClient(t, mux)

	var out bytes.Buffer
	err := runSnapshotList(context.Background(), c, "p", "", &out)
	require.NoError(t, err)

	lines := nonEmptyLines(out.String())
	require.Len(t, lines, 3, "one header + one line per declared entry")
	assert.Regexp(t, `^NAME\s+MODE\s+STATUS\s+LATEST\s+SIZE\s+SOURCE\s*$`, lines[0])
	assert.NotContains(t, lines[0], "ORPHAN", "no orphan rows → no ORPHAN column")
	assert.Regexp(t, `pg\s+stream\s+ok\s+2026-11-16 03:00`, lines[1])
	assert.Regexp(t, `mysql\s+stream\s+ok\s+2026-11-15 03:00`, lines[2])
}

func TestDevmSnapshotList_ShowsModeStatusOrphan(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/snapshot/list", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]snapshot.Meta{
			{TS: "2026-11-17T1000-00", ArchiveName: "claude", Mode: snapshot.ModePath, Status: snapshot.StatusOk, SizeBytes: 1024, Source: snapshot.SourceCLI},
			{TS: "2026-11-17T0900-00", ArchiveName: "pg", Mode: snapshot.ModeStream, Status: snapshot.StatusPostFailed, SizeBytes: 2048, Source: snapshot.SourceCron},
			{TS: "2026-11-15T0000-00", ArchiveName: "stale", Orphan: true},
		})
	})
	c := newClient(t, mux)

	var out bytes.Buffer
	require.NoError(t, runSnapshotList(context.Background(), c, "p", "", &out))
	s := out.String()
	assert.Regexp(t, `claude\s+path\s+ok\s+2026-11-17 10:00`, s)
	assert.Regexp(t, `pg\s+stream\s+post_failed\s+2026-11-17 09:00`, s)
	assert.Regexp(t, `stale\s+.*orphan`, s)
	assert.Regexp(t, `^NAME\s+MODE\s+STATUS\s+LATEST\s+SIZE\s+SOURCE\s+ORPHAN\s*$`, nonEmptyLines(s)[0])
}

func TestSnapshotList_WithArg_ListsArchives(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/snapshot/list", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "p", r.URL.Query().Get("project"))
		require.Equal(t, "pg", r.URL.Query().Get("entry"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]snapshot.Meta{
			{TS: "2026-11-16T0300-00", ArchiveName: "a.zst", Source: snapshot.SourceCron, SizeBytes: 3000},
			{TS: "2026-11-15T0300-00", ArchiveName: "b.zst", Source: snapshot.SourceCLI, SizeBytes: 2000},
			{TS: "2026-11-14T0300-00", ArchiveName: "c.zst", Source: snapshot.SourceCLI, SizeBytes: 1000},
		})
	})
	c := newClient(t, mux)

	var out bytes.Buffer
	err := runSnapshotList(context.Background(), c, "p", "pg", &out)
	require.NoError(t, err)

	lines := nonEmptyLines(out.String())
	require.Len(t, lines, 3, "one line per archive, newest first")
	// Newest first — the fake server returned them pre-sorted.
	assert.Contains(t, lines[0], "2026-11-16T0300-00")
	assert.Contains(t, lines[1], "2026-11-15T0300-00")
	assert.Contains(t, lines[2], "2026-11-14T0300-00")
	assert.Contains(t, lines[0], "cron")
	assert.Contains(t, lines[1], "cli")
}

func TestSnapshotRestore_DefaultsLatest(t *testing.T) {
	var gotBody struct {
		Project string `json:"project"`
		Entry   string `json:"entry"`
		TS      string `json:"ts"`
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/snapshot/restore", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"restored":true}`))
	})
	c := newClient(t, mux)

	var out bytes.Buffer
	err := runSnapshotRestore(context.Background(), c, "p", "pg", "", false, &out)
	require.NoError(t, err)
	assert.Equal(t, "p", gotBody.Project)
	assert.Equal(t, "pg", gotBody.Entry)
	assert.Equal(t, "", gotBody.TS, "unspecified ts means latest — server-side resolution")
	assert.Contains(t, out.String(), "restored")
}

// TestSnapshotRestore_NothingToRestore pins the install-hook case: the
// daemon reports the archive did not exist. The CLI must print nothing
// to stdout and return without error.
func TestSnapshotRestore_NothingToRestore(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/snapshot/restore", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"restored":false}`))
	})
	c := newClient(t, mux)

	var out bytes.Buffer
	err := runSnapshotRestore(context.Background(), c, "p", "pg", "", false, &out)
	require.NoError(t, err)
	assert.Empty(t, out.String(), "stdout stays empty — the diagnostic goes to stderr")
}

func TestSnapshotRestore_ExplicitTS(t *testing.T) {
	var gotBody struct {
		Project string `json:"project"`
		Entry   string `json:"entry"`
		TS      string `json:"ts"`
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/snapshot/restore", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		_, _ = w.Write([]byte(`{"restored":true}`))
	})
	c := newClient(t, mux)

	err := runSnapshotRestore(context.Background(), c, "p", "pg", "2026-01-02T0100-00", false, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, "2026-01-02T0100-00", gotBody.TS)
}

func TestSnapshotPrune_WithKeep(t *testing.T) {
	var gotBody struct {
		Project string `json:"project"`
		Entry   string `json:"entry"`
		Keep    int    `json:"keep"`
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/snapshot/prune", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"removed": []string{"2026-10-01T0300-00"}})
	})
	c := newClient(t, mux)

	var out bytes.Buffer
	err := runSnapshotPrune(context.Background(), c, "p", "pg", 3, &out)
	require.NoError(t, err)
	assert.Equal(t, "p", gotBody.Project)
	assert.Equal(t, "pg", gotBody.Entry)
	assert.Equal(t, 3, gotBody.Keep)
	assert.Contains(t, out.String(), "pruned 1")
	assert.Contains(t, out.String(), "2026-10-01T0300-00")
}

// TestSnapshotPrune_RequiresName pins the cobra arg validation: a bare
// `devm snapshot prune` (no name) must refuse before any dispatch so the
// user sees the shape error and no "name is empty" leak reaches the
// daemon. We assert the shape of cobra's message ("accepts … arg(s)")
// rather than the literal — the value is the behavior, not the text.
func TestSnapshotPrune_RequiresName(t *testing.T) {
	var outBuf, errBuf bytes.Buffer
	rootCmd.SetOut(&outBuf)
	rootCmd.SetErr(&errBuf)
	rootCmd.SetArgs([]string{"snapshot", "prune", "--keep", "3"})
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})
	err := rootCmd.Execute()
	require.Error(t, err)
	msg := err.Error()
	// cobra.ExactArgs(1) produces "accepts N arg(s), received M".
	assert.Contains(t, msg, "accepts")
	assert.Contains(t, msg, "arg")
}

func TestSnapshotShow_PrintsMeta(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/snapshot/show", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "p", r.URL.Query().Get("project"))
		require.Equal(t, "pg", r.URL.Query().Get("entry"))
		require.Equal(t, "", r.URL.Query().Get("ts"), "empty ts means latest — server-side resolution")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snapshot.Meta{
			TS:          "2026-11-16T0300-00",
			HookName:    "backup_pg",
			ArchiveName: "2026-11-16T0300-00.zst",
			Source:      snapshot.SourceCLI,
			ExitCode:    0,
			DurationMs:  2400,
			SizeBytes:   1258291,
		})
	})
	c := newClient(t, mux)

	var out bytes.Buffer
	err := runSnapshotShow(context.Background(), c, "p", "pg", "", &out)
	require.NoError(t, err)
	s := out.String()
	for _, needle := range []string{
		"ts:",
		"2026-11-16T0300-00",
		"hook_name:",
		"backup_pg",
		"archive_name:",
		"2026-11-16T0300-00.zst",
		"source:",
		"cli",
		"exit_code:",
		"duration:",
		"size:",
	} {
		assert.Contains(t, s, needle, "show output should include %q", needle)
	}
}

func TestSnapshotRestore_ForceFlagReachesWire(t *testing.T) {
	var gotBody struct {
		Project string `json:"project"`
		Entry   string `json:"entry"`
		TS      string `json:"ts"`
		Force   bool   `json:"force"`
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/snapshot/restore", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		_, _ = w.Write([]byte("{}"))
	})
	c := newClient(t, mux)

	err := runSnapshotRestore(context.Background(), c, "p", "pg", "", true, io.Discard)
	require.NoError(t, err)
	assert.True(t, gotBody.Force, "--force must reach the daemon as force=true")
}

func TestDevmSnapshotClearCmd_PrintsCount(t *testing.T) {
	var gotBody struct {
		Project string `json:"project"`
		Entry   string `json:"entry"`
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/snapshot/clear", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"removed":1}`)
	})
	c := newClient(t, mux)

	var out bytes.Buffer
	require.NoError(t, runSnapshotClear(context.Background(), c, "p", "pg", &out))
	assert.Equal(t, "p", gotBody.Project)
	assert.Equal(t, "pg", gotBody.Entry)
	assert.Equal(t, "cleared 1 archive(s) for pg\n", out.String())
}

func TestSnapshotClear_SurfacesDaemonError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/snapshot/clear", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "boom"})
	})
	c := newClient(t, mux)

	err := runSnapshotClear(context.Background(), c, "p", "pg", io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}
