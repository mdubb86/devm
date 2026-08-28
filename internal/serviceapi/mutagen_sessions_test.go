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

// ---------- SetupPhase / StopPhase / TeardownPhase test scaffolding ----------

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
					}, len(s.listSessions))
					for i, sess := range s.listSessions {
						rows[i] = struct {
							Identifier string `json:"identifier"`
							Name       string `json:"name"`
							Status     string `json:"status"`
						}{sess.ID, sess.Name, sess.Status}
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

func TestSetupPhase_ColdStartClonesThenCreates(t *testing.T) {
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

	err := SetupPhase(context.Background(), cli, cfg, "myproj", entities, exec,
		"myproj.test", "http://127.0.0.1:5555", "/etc/ssl/certs/devm-ca.crt")
	require.NoError(t, err)

	require.Len(t, sc.createArgs, 1)
	args := sc.createArgs[0]
	assert.Contains(t, args, "--name")
	assert.Contains(t, args, "devm-myproj-app")
	assert.Contains(t, args, "devm@myproj.test:/home/devm/app")
}

func TestSetupPhase_WarmStartResumesPausedSession(t *testing.T) {
	cfg := testSessionsIdentity(t)
	sc := &scriptedCLI{
		listSessions: []mutagen.SyncSession{
			{ID: "sess-1", Name: "devm-myproj-app", Status: "paused"},
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

	err := SetupPhase(context.Background(), cli, cfg, "myproj", entities, exec,
		"myproj.test", "http://127.0.0.1:5555", "/etc/ssl/certs/devm-ca.crt")
	require.NoError(t, err)

	assert.Equal(t, []string{"sess-1"}, sc.resumeCalls)
	assert.Empty(t, sc.createArgs, "warm resume must not regenerate config or create a session")
}

func TestSetupPhase_AlignedContentCreatesSession(t *testing.T) {
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

	err = SetupPhase(context.Background(), cli, cfg, "myproj", entities, exec,
		"myproj.test", "http://127.0.0.1:5555", "/etc/ssl/certs/devm-ca.crt")
	require.NoError(t, err)

	require.Len(t, sc.createArgs, 1)
}

func TestSetupPhase_DivergentGuardRejects(t *testing.T) {
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

	err = SetupPhase(context.Background(), cli, cfg, "myproj", entities, exec,
		"myproj.test", "http://127.0.0.1:5555", "/etc/ssl/certs/devm-ca.crt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "app")
	assert.Empty(t, sc.createArgs, "guard rejection must not create a session")
}

func TestSetupPhase_NoMirrorEntity_ClonesButNoSession(t *testing.T) {
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

	err := SetupPhase(context.Background(), cli, cfg, "myproj", entities, exec,
		"myproj.test", "http://127.0.0.1:5555", "/etc/ssl/certs/devm-ca.crt")
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

func TestSetupPhase_NoMirrorEntity_AlreadyClonedSkipsClone(t *testing.T) {
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

	err := SetupPhase(context.Background(), cli, cfg, "myproj", entities, exec,
		"myproj.test", "http://127.0.0.1:5555", "/etc/ssl/certs/devm-ca.crt")
	require.NoError(t, err)

	assert.Empty(t, cloneScripts, "an already-populated guest dir must not be re-cloned")
	assert.Empty(t, sc.createArgs)
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
