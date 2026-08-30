package serviceapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/mutagen"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------- SessionName / SessionNamePrefix ----------

func TestSessionName_Format(t *testing.T) {
	assert.Equal(t, "devm-myproj-backend", SessionName("myproj", "backend"))
}

func TestSessionNamePrefix_Format(t *testing.T) {
	assert.Equal(t, "devm-myproj-", SessionNamePrefix("myproj"))
}

// ---------- BuildEntities ----------

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }

func TestBuildEntities_PrimaryOnly(t *testing.T) {
	cfg := &schema.Config{
		Repos: map[string]schema.RepoConfig{
			"app": {URL: strPtr("git@github.com:me/app.git"), Primary: boolPtr(true)},
		},
	}
	entities, err := BuildEntities(cfg, "/Users/me/whatever")
	require.NoError(t, err)
	require.Len(t, entities, 1)
	assert.Equal(t, "app", entities[0].Label)
	assert.Equal(t, "/home/devm/app", entities[0].GuestPath)
	require.NotNil(t, entities[0].Repo)
	assert.Equal(t, "git@github.com:me/app.git", entities[0].Repo.URL)
}

func TestBuildEntities_PrimaryPlusSecondaryVolumeTrue(t *testing.T) {
	cfg := &schema.Config{
		Repos: map[string]schema.RepoConfig{
			"app":  {URL: strPtr("git@github.com:me/app.git"), Primary: boolPtr(true)},
			"data": {URL: strPtr("git@github.com:me/data.git"), Volume: boolPtr(true)},
		},
	}
	entities, err := BuildEntities(cfg, "/Users/me/whatever")
	require.NoError(t, err)
	require.Len(t, entities, 2)

	labels := map[string]bool{}
	for _, e := range entities {
		labels[e.Label] = true
	}
	assert.True(t, labels["app"])
	assert.True(t, labels["data"])
}

func TestBuildEntities_VolumeFalseSecondary_IncludedAsNoMirror(t *testing.T) {
	cfg := &schema.Config{
		Repos: map[string]schema.RepoConfig{
			"app":  {URL: strPtr("git@github.com:me/app.git"), Primary: boolPtr(true)},
			"data": {URL: strPtr("git@github.com:me/data.git"), Volume: boolPtr(false)},
		},
	}
	entities, err := BuildEntities(cfg, "/Users/me/whatever")
	require.NoError(t, err)
	require.Len(t, entities, 2)

	var app, data *SessionEntity
	for i := range entities {
		switch entities[i].Label {
		case "app":
			app = &entities[i]
		case "data":
			data = &entities[i]
		}
	}
	require.NotNil(t, app)
	require.NotNil(t, data)
	assert.False(t, app.NoMirror, "primary is always mirrored")
	assert.True(t, data.NoMirror, "volume:false secondary is cold-start-clone only, no mirror session")
	require.NotNil(t, data.Repo)
	assert.Equal(t, "git@github.com:me/data.git", data.Repo.URL)
	assert.Equal(t, "/home/devm/data", data.GuestPath)
}

func TestBuildEntities_VolumeNilSecondary_IncludedAsNoMirror(t *testing.T) {
	cfg := &schema.Config{
		Repos: map[string]schema.RepoConfig{
			"app":  {URL: strPtr("git@github.com:me/app.git"), Primary: boolPtr(true)},
			"data": {URL: strPtr("git@github.com:me/data.git")},
		},
	}
	entities, err := BuildEntities(cfg, "/Users/me/whatever")
	require.NoError(t, err)
	require.Len(t, entities, 2)

	var data *SessionEntity
	for i := range entities {
		if entities[i].Label == "data" {
			data = &entities[i]
		}
	}
	require.NotNil(t, data)
	assert.True(t, data.NoMirror, "volume-unset secondary defaults to cold-start-clone only")
}

func TestBuildEntities_VolumesIncluded(t *testing.T) {
	cfg := &schema.Config{
		Repos: map[string]schema.RepoConfig{
			"app": {URL: strPtr("git@github.com:me/app.git"), Primary: boolPtr(true)},
		},
		Volumes: map[string]schema.Volume{
			"pg-data": {Path: "/var/lib/postgresql/data"},
		},
	}
	entities, err := BuildEntities(cfg, "/Users/me/whatever")
	require.NoError(t, err)
	require.Len(t, entities, 2)

	var vol *SessionEntity
	for i := range entities {
		if entities[i].Label == "data" {
			vol = &entities[i]
		}
	}
	require.NotNil(t, vol, "volume label should derive from leaf of path")
	assert.Equal(t, "/var/lib/postgresql/data", vol.GuestPath)
	assert.Nil(t, vol.Repo)
}

func TestBuildEntities_URLNilPrimaryLabelFromMacCwd(t *testing.T) {
	// DeriveRepoURL shells out to `git remote get-url origin`, so the
	// URL-nil primary path needs a real git checkout with an origin
	// remote to resolve against.
	macCwd := filepath.Join(t.TempDir(), "sewtrue")
	require.NoError(t, os.MkdirAll(macCwd, 0o755))
	runGit := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", macCwd}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	runGit("init", "-q")
	runGit("remote", "add", "origin", "git@github.com:me/sewtrue.git")

	cfg := &schema.Config{
		Repos: map[string]schema.RepoConfig{
			"app": {},
		},
	}
	entities, err := BuildEntities(cfg, macCwd)
	require.NoError(t, err)
	require.Len(t, entities, 1)
	assert.Equal(t, "sewtrue", entities[0].Label)
	assert.Equal(t, "/home/devm/sewtrue", entities[0].GuestPath)
	require.NotNil(t, entities[0].Repo)
	assert.Equal(t, "git@github.com:me/sewtrue.git", entities[0].Repo.URL)
}

func TestBuildEntities_LabelOverrideRespected(t *testing.T) {
	cfg := &schema.Config{
		Repos: map[string]schema.RepoConfig{
			"app": {URL: strPtr("git@github.com:me/app.git"), Label: strPtr("custom-label"), Primary: boolPtr(true)},
		},
	}
	entities, err := BuildEntities(cfg, "/Users/me/whatever")
	require.NoError(t, err)
	require.Len(t, entities, 1)
	assert.Equal(t, "custom-label", entities[0].Label)
	assert.Equal(t, "/home/devm/custom-label", entities[0].GuestPath)
}

// ---------- SetupVolumesPhase / SetupReposPhase / StopPhase / TeardownPhase test scaffolding ----------

// scriptedCLI builds a *mutagen.CLI whose Exec dispatches on the mutagen
// subcommand (args[0] args[1]) to canned behavior, recording every
// sync-create/resume call it sees.
type scriptedCLI struct {
	listSessions  []mutagen.SyncSession
	createCalls   []mutagen.SyncSession // Name/alpha stored via extra field below
	createArgs    [][]string
	resumeCalls   []string
	flushCalls    []string
	pauseCalls    []string
	terminateCall []string

	// flushErrs, keyed by session ID, makes the "flush" dispatch return
	// a nonzero exit + this message instead of succeeding — used to
	// exercise FlushAll's fail-fast path.
	flushErrs map[string]string
}

func (s *scriptedCLI) build() *mutagen.CLI {
	return &mutagen.CLI{
		Binary: "mutagen",
		Exec: func(bin string, args []string, env []string) (string, string, int, error) {
			if len(args) >= 2 && args[0] == "sync" {
				switch args[1] {
				case "list":
					rows := make([]struct {
						Identifier string `json:"identifier"`
						Name       string `json:"name"`
						Status     string `json:"status"`
						Paused     bool   `json:"paused"`
					}, len(s.listSessions))
					for i, sess := range s.listSessions {
						rows[i] = struct {
							Identifier string `json:"identifier"`
							Name       string `json:"name"`
							Status     string `json:"status"`
							Paused     bool   `json:"paused"`
						}{sess.ID, sess.Name, sess.Status, sess.Paused}
					}
					b, _ := json.Marshal(rows)
					return string(b), "", 0, nil
				case "create":
					s.createArgs = append(s.createArgs, append([]string{}, args...))
					return "Created session sess-new\n", "", 0, nil
				case "resume":
					s.resumeCalls = append(s.resumeCalls, args[2])
					return "", "", 0, nil
				case "flush":
					s.flushCalls = append(s.flushCalls, args[2])
					if msg, ok := s.flushErrs[args[2]]; ok {
						return "", msg, 1, nil
					}
					return "", "", 0, nil
				case "pause":
					s.pauseCalls = append(s.pauseCalls, args[2])
					return "", "", 0, nil
				case "terminate":
					s.terminateCall = append(s.terminateCall, args[2])
					return "", "", 0, nil
				}
			}
			return "", "", 0, fmt.Errorf("scriptedCLI: unhandled args %v", args)
		},
	}
}

// scriptedGuestExec dispatches on script content: mkdir/install/clone
// scripts always succeed; scan scripts (identified by the guest scan
// script's "find ." probe) return a canned "count=N size=N hash=H" line
// depending on whether guestEmpty is set.
func scriptedGuestExec(guestEmpty bool) GuestExec {
	return func(script string) (string, string, int, error) {
		if strings.Contains(script, "find .") {
			if guestEmpty {
				return "count=0 size=0 hash=-\n", "", 0, nil
			}
			return "count=3 size=300 hash=abc\n", "", 0, nil
		}
		return "", "", 0, nil
	}
}

func testSessionsIdentity(t *testing.T) identity.Config {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	return identity.Config{Name: "devm-test-sessions"}
}

func TestSetupPhases_ColdStartClonesThenCreates(t *testing.T) {
	cfg := testSessionsIdentity(t)
	sc := &scriptedCLI{} // empty sync list
	cli := sc.build()

	entities := []SessionEntity{
		{
			Label:     "app",
			GuestPath: "/home/devm/app",
			Repo:      &SessionRepoInfo{URL: "git@github.com:me/app.git", Secret: "gh"},
		},
	}

	exec := scriptedGuestExec(true) // mac mirror is freshly created (empty), guest empty too

	err := SetupVolumesPhase(context.Background(), cli, cfg, "myproj", entities, exec, "myproj.test")
	require.NoError(t, err)
	err = SetupReposPhase(context.Background(), cfg, "myproj", entities, exec, "http://127.0.0.1:5555", "/etc/ssl/certs/devm-ca.crt")
	require.NoError(t, err)

	require.Len(t, sc.createArgs, 1)
	args := sc.createArgs[0]
	assert.Contains(t, args, "--name")
	assert.Contains(t, args, "devm-myproj-app")
	assert.Contains(t, args, "devm@myproj.test:/home/devm/app")
}

func TestSetupVolumesPhase_WarmStartResumesPausedSession(t *testing.T) {
	cfg := testSessionsIdentity(t)
	sc := &scriptedCLI{
		// Paused=true is what mutagen actually reports for a
		// user-paused session; the Status field stays a transport
		// state and is NEVER the literal "paused". Pinned in
		// e2e/test_mutagen_contract_04 + 05.
		listSessions: []mutagen.SyncSession{
			{ID: "sess-1", Name: "devm-myproj-app", Status: "Disconnected", Paused: true},
		},
	}
	cli := sc.build()

	entities := []SessionEntity{
		{
			Label:     "app",
			GuestPath: "/home/devm/app",
			Repo:      &SessionRepoInfo{URL: "git@github.com:me/app.git", Secret: "gh"},
		},
	}

	exec := scriptedGuestExec(false) // both sides populated + aligned (same scan values)

	err := SetupVolumesPhase(context.Background(), cli, cfg, "myproj", entities, exec, "myproj.test")
	require.NoError(t, err)
	err = SetupReposPhase(context.Background(), cfg, "myproj", entities, exec, "http://127.0.0.1:5555", "/etc/ssl/certs/devm-ca.crt")
	require.NoError(t, err)

	assert.Equal(t, []string{"sess-1"}, sc.resumeCalls)
	assert.Empty(t, sc.createArgs, "warm resume must not regenerate config or create a session")
}

// TestSetupPhases_WarmAttachRepoDoesNotCloneAgain locks in the safety
// of the current SetupVolumesPhase/SetupReposPhase split for a
// warm-attach repo entity: SetupVolumesPhase finds an existing mutagen
// session via SyncList (so it skips the guard and never creates a
// session), and SetupReposPhase then scans both sides — Mac mirror
// content persisted across stop/start, guest content from the earlier
// clone — finds both non-empty, and must not invoke the clone seam.
func TestSetupPhases_WarmAttachRepoDoesNotCloneAgain(t *testing.T) {
	cfg := testSessionsIdentity(t)
	sc := &scriptedCLI{
		listSessions: []mutagen.SyncSession{
			{ID: "sess-1", Name: "devm-myproj-app", Status: "watching", Paused: false},
		},
	}
	cli := sc.build()

	entities := []SessionEntity{
		{
			Label:     "app",
			GuestPath: "/home/devm/app",
			Repo:      &SessionRepoInfo{URL: "git@github.com:me/app.git", Secret: "gh"},
		},
	}

	// Mac mirror side: populated, representing state that persisted
	// across a prior stop/start.
	macDir, _, err := ensureMirrorDir(cfg, "myproj", "app")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(macDir, "foo.txt"), []byte("test"), 0o644))

	// Guest side: also populated, representing the warm-attached
	// state left by the original clone.
	exec := func(script string) (string, string, int, error) {
		if strings.Contains(script, "find .") {
			return "count=3 size=300 hash=abc\n", "", 0, nil
		}
		return "", "", 0, nil
	}

	var cloneCalls []string
	origClone := cloneRepoInGuestFn
	cloneRepoInGuestFn = func(exec GuestExec, req CloneRequest) error {
		cloneCalls = append(cloneCalls, req.GuestTargetPath)
		return nil
	}
	t.Cleanup(func() { cloneRepoInGuestFn = origClone })

	err = SetupVolumesPhase(context.Background(), cli, cfg, "myproj", entities, exec, "myproj.test")
	require.NoError(t, err)
	err = SetupReposPhase(context.Background(), cfg, "myproj", entities, exec, "http://127.0.0.1:5555", "/etc/ssl/certs/devm-ca.crt")
	require.NoError(t, err)

	assert.Empty(t, sc.createArgs, "warm-attached session must not be recreated")
	assert.Empty(t, sc.resumeCalls, "an already-active (non-paused) session must not be resumed")
	assert.Empty(t, cloneCalls, "warm-attach composition must short-circuit the clone seam when both sides are non-empty")
}

func TestSetupPhases_AlignedContentCreatesSession(t *testing.T) {
	cfg := testSessionsIdentity(t)
	sc := &scriptedCLI{} // no existing sessions
	cli := sc.build()

	entities := []SessionEntity{
		{
			Label:     "app",
			GuestPath: "/home/devm/app",
			Repo:      &SessionRepoInfo{URL: "git@github.com:me/app.git", Secret: "gh"},
		},
	}

	// Pre-populate the Mac mirror with one real file, then have the
	// fake guest scan report the exact same count/size/hash a real
	// ScanGuest would compute for an identical tree — a genuinely
	// aligned, non-empty pair on both sides.
	macDir, _, err := ensureMirrorDir(cfg, "myproj", "app")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(macDir, "foo.txt"), []byte("test"), 0o644))
	wantHash := hashTopSample([]string{"foo.txt"})

	exec := func(script string) (string, string, int, error) {
		if strings.Contains(script, "find .") {
			return fmt.Sprintf("count=1 size=4 hash=%s\n", wantHash), "", 0, nil
		}
		return "", "", 0, nil
	}

	err = SetupVolumesPhase(context.Background(), cli, cfg, "myproj", entities, exec, "myproj.test")
	require.NoError(t, err)
	err = SetupReposPhase(context.Background(), cfg, "myproj", entities, exec, "http://127.0.0.1:5555", "/etc/ssl/certs/devm-ca.crt")
	require.NoError(t, err)

	require.Len(t, sc.createArgs, 1)
}

func TestSetupVolumesPhase_DivergentGuardRejects(t *testing.T) {
	cfg := testSessionsIdentity(t)
	sc := &scriptedCLI{}
	cli := sc.build()

	entities := []SessionEntity{
		{
			Label:     "app",
			GuestPath: "/home/devm/app",
			Repo:      &SessionRepoInfo{URL: "git@github.com:me/app.git", Secret: "gh"},
		},
	}

	// Pre-populate the Mac mirror with one file so ScanMac reports a
	// non-empty side, then have the guest report a different, larger
	// non-empty side — GuardCheck must reject the size mismatch.
	macDir, _, err := ensureMirrorDir(cfg, "myproj", "app")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(macDir, "one-file.txt"), []byte("hi"), 0o644))

	exec := func(script string) (string, string, int, error) {
		if strings.Contains(script, "find .") {
			return "count=5 size=500 hash=xyz\n", "", 0, nil
		}
		return "", "", 0, nil
	}

	err = SetupVolumesPhase(context.Background(), cli, cfg, "myproj", entities, exec, "myproj.test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "app")
	assert.Empty(t, sc.createArgs, "guard rejection must not create a session")
}

func TestSetupPhases_NoMirrorEntity_ClonesButNoSession(t *testing.T) {
	cfg := testSessionsIdentity(t)
	sc := &scriptedCLI{} // empty sync list — sync/create must NEVER be called
	cli := sc.build()

	var cloneScripts []string
	exec := func(script string) (string, string, int, error) {
		if strings.Contains(script, "git clone") {
			cloneScripts = append(cloneScripts, script)
		}
		if strings.Contains(script, "find .") {
			return "count=0 size=0 hash=-\n", "", 0, nil
		}
		return "", "", 0, nil
	}

	entities := []SessionEntity{
		{
			Label:     "data",
			GuestPath: "/home/devm/data",
			NoMirror:  true,
			Repo:      &SessionRepoInfo{URL: "git@github.com:me/data.git", Secret: "gh"},
		},
	}

	err := SetupVolumesPhase(context.Background(), cli, cfg, "myproj", entities, exec, "myproj.test")
	require.NoError(t, err)
	err = SetupReposPhase(context.Background(), cfg, "myproj", entities, exec, "http://127.0.0.1:5555", "/etc/ssl/certs/devm-ca.crt")
	require.NoError(t, err)

	require.Len(t, cloneScripts, 1, "cold-start clone must run for a NoMirror entity")
	assert.Contains(t, cloneScripts[0], "git@github.com:me/data.git")
	assert.Empty(t, sc.createArgs, "mutagen sync create must never be called for a NoMirror entity")
	assert.Empty(t, sc.resumeCalls, "mutagen sync resume must never be called for a NoMirror entity")

	// No Mac mirror dir should have been created for a NoMirror entity.
	macMirror, _, err := ensureMirrorDir(cfg, "myproj", "data")
	require.NoError(t, err)
	entriesInMirror, err := os.ReadDir(macMirror)
	require.NoError(t, err)
	assert.Empty(t, entriesInMirror, "NoMirror entity must not populate a mac mirror dir")
}

func TestSetupReposPhase_NoMirrorEntity_AlreadyClonedSkipsClone(t *testing.T) {
	cfg := testSessionsIdentity(t)
	sc := &scriptedCLI{}
	cli := sc.build()

	var cloneScripts []string
	exec := func(script string) (string, string, int, error) {
		if strings.Contains(script, "git clone") {
			cloneScripts = append(cloneScripts, script)
		}
		if strings.Contains(script, "find .") {
			return "count=3 size=300 hash=abc\n", "", 0, nil
		}
		return "", "", 0, nil
	}

	entities := []SessionEntity{
		{
			Label:     "data",
			GuestPath: "/home/devm/data",
			NoMirror:  true,
			Repo:      &SessionRepoInfo{URL: "git@github.com:me/data.git", Secret: "gh"},
		},
	}

	err := SetupVolumesPhase(context.Background(), cli, cfg, "myproj", entities, exec, "myproj.test")
	require.NoError(t, err)
	err = SetupReposPhase(context.Background(), cfg, "myproj", entities, exec, "http://127.0.0.1:5555", "/etc/ssl/certs/devm-ca.crt")
	require.NoError(t, err)

	assert.Empty(t, cloneScripts, "an already-populated guest dir must not be re-cloned")
	assert.Empty(t, sc.createArgs)
}

// TestSetupReposPhase_ClonesOnlyEmptyRepos exercises SetupReposPhase in
// isolation across three entity shapes: a pure volume (no clone path
// at all), a repo with both sides empty (must clone), and a repo whose
// guest side already has content (must not clone).
func TestSetupReposPhase_ClonesOnlyEmptyRepos(t *testing.T) {
	cfg := testSessionsIdentity(t)

	var cloneCalls []string
	origClone := cloneRepoInGuestFn
	cloneRepoInGuestFn = func(exec GuestExec, req CloneRequest) error {
		cloneCalls = append(cloneCalls, req.GuestTargetPath)
		return nil
	}
	defer func() { cloneRepoInGuestFn = origClone }()

	entities := []SessionEntity{
		{Label: "vol1", GuestPath: "/vol1"}, // pure volume
		{Label: "repoEmpty", GuestPath: "/home/devm/repoEmpty", Repo: &SessionRepoInfo{URL: "https://github.com/x/e.git", Secret: "gh_stub"}},
		{Label: "repoPopulated", GuestPath: "/home/devm/repoPopulated", Repo: &SessionRepoInfo{URL: "https://github.com/x/p.git", Secret: "gh_stub"}},
	}

	// Guest-side scan fake: repoPopulated's guest path reports a
	// non-empty scan; every other path (including the mac-mirror-only
	// probes ScanMac never issues over exec) reports empty.
	exec := func(script string) (string, string, int, error) {
		if strings.Contains(script, "find .") {
			if strings.Contains(script, "repoPopulated") {
				return "count=5 size=500 hash=abc\n", "", 0, nil
			}
			return "count=0 size=0 hash=-\n", "", 0, nil
		}
		return "", "", 0, nil
	}

	err := SetupReposPhase(context.Background(), cfg, "myproj", entities, exec, "http://mac-loopback:tunnel", "/etc/ssl/certs/devm.crt")
	require.NoError(t, err)

	assert.Equal(t, []string{"/home/devm/repoEmpty"}, cloneCalls, "clone calls = repoEmpty only")
}

func TestSetupVolumesPhase_UniformSessionSetup(t *testing.T) {
	cfg := testSessionsIdentity(t)
	sc := &scriptedCLI{} // no existing sessions
	cli := sc.build()

	// All three entities get a session — no clone-branch coupling.
	entities := []SessionEntity{
		{Label: "vol1", GuestPath: "/vol1"},
		{Label: "repoEmpty", GuestPath: "/home/devm/repoEmpty", Repo: &SessionRepoInfo{URL: "https://github.com/x/e.git", Secret: "gh_stub"}},
		{Label: "repoPopulated", GuestPath: "/home/devm/repoPopulated", Repo: &SessionRepoInfo{URL: "https://github.com/x/p.git", Secret: "gh_stub"}},
	}

	err := SetupVolumesPhase(context.Background(), cli, cfg, "myproj", entities, scriptedGuestExec(true), "myproj.test")
	require.NoError(t, err)

	assert.Len(t, sc.createArgs, 3, "sessions created for all entities including repos")
}

// ---------- StopPhase / TeardownPhase ----------

func TestStopPhase_FlushAndPauseAll(t *testing.T) {
	sc := &scriptedCLI{
		listSessions: []mutagen.SyncSession{
			{ID: "s1", Name: "devm-myproj-app", Status: "watching"},
			{ID: "s2", Name: "devm-myproj-data", Status: "watching"},
			{ID: "s3", Name: "devm-myproj-pg", Status: "watching"},
		},
	}
	cli := sc.build()

	err := StopPhase(cli, "myproj")
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"s1", "s2", "s3"}, sc.flushCalls)
	assert.ElementsMatch(t, []string{"s1", "s2", "s3"}, sc.pauseCalls)
}

// ---------- FlushAll ----------

func TestFlushAll_FlushesActiveOnlyAndFailsFast(t *testing.T) {
	sc := &scriptedCLI{
		listSessions: []mutagen.SyncSession{
			{ID: "s1", Name: "devm-myproj-app", Status: "watching", Paused: false},
			{ID: "s2", Name: "devm-myproj-data", Status: "watching", Paused: true},
			{ID: "s3", Name: "devm-myproj-pg", Status: "watching", Paused: false},
			{ID: "s4", Name: "devm-myproj-extra", Status: "watching", Paused: false},
		},
		flushErrs: map[string]string{"s3": "boom"},
	}
	cli := sc.build()

	err := FlushAll(cli, "myproj")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")

	assert.Equal(t, []string{"s1", "s3"}, sc.flushCalls,
		"paused s2 must be skipped, and the loop must stop at the first error (s4 never reached)")
}

func TestFlushAll_FlushesAllNonPausedOnSuccess(t *testing.T) {
	sc := &scriptedCLI{
		listSessions: []mutagen.SyncSession{
			{ID: "s1", Name: "devm-myproj-app", Status: "watching", Paused: false},
			{ID: "s2", Name: "devm-myproj-data", Status: "watching", Paused: true},
			{ID: "s3", Name: "devm-myproj-pg", Status: "watching", Paused: false},
		},
	}
	cli := sc.build()

	err := FlushAll(cli, "myproj")
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"s1", "s3"}, sc.flushCalls)
}

func TestTeardownPhase_TerminateAll(t *testing.T) {
	sc := &scriptedCLI{
		listSessions: []mutagen.SyncSession{
			{ID: "s1", Name: "devm-myproj-app", Status: "watching"},
			{ID: "s2", Name: "devm-myproj-data", Status: "watching"},
			{ID: "s3", Name: "devm-myproj-pg", Status: "watching"},
		},
	}
	cli := sc.build()

	err := TeardownPhase(cli, "myproj")
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"s1", "s2", "s3"}, sc.terminateCall)
}
