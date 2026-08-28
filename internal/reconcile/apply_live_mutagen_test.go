package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/mutagen"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------- fakes ----------

// fakeMutagenCLI builds a *mutagen.CLI whose Exec dispatches on the
// mutagen subcommand to canned behavior, recording every
// create/flush/terminate/resume call it sees. Mirrors
// serviceapi/mutagen_sessions_test.go's scriptedCLI.
type fakeMutagenCLI struct {
	listSessions   []mutagen.SyncSession
	createArgs     [][]string
	resumeCalls    []string
	flushCalls     []string
	terminateCalls []string
}

func (f *fakeMutagenCLI) build() *mutagen.CLI {
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
					}, len(f.listSessions))
					for i, sess := range f.listSessions {
						rows[i] = struct {
							Identifier string `json:"identifier"`
							Name       string `json:"name"`
							Status     string `json:"status"`
						}{sess.ID, sess.Name, sess.Status}
					}
					b, _ := json.Marshal(rows)
					return string(b), "", 0, nil
				case "create":
					f.createArgs = append(f.createArgs, append([]string{}, args...))
					return "Created session sess-new\n", "", 0, nil
				case "resume":
					f.resumeCalls = append(f.resumeCalls, args[2])
					return "", "", 0, nil
				case "flush":
					f.flushCalls = append(f.flushCalls, args[2])
					return "", "", 0, nil
				case "terminate":
					f.terminateCalls = append(f.terminateCalls, args[2])
					// Simulate the session actually disappearing so a
					// subsequent SyncList in the same call (e.g. a
					// terminate-then-recreate flow) doesn't still see it.
					kept := f.listSessions[:0]
					for _, s := range f.listSessions {
						if s.ID != args[2] {
							kept = append(kept, s)
						}
					}
					f.listSessions = kept
					return "", "", 0, nil
				}
			}
			return "", "", 0, fmt.Errorf("fakeMutagenCLI: unhandled args %v", args)
		},
	}
}

// recordingExec is a fake GuestExec: it records every script it's
// asked to run and answers the fixed guest scan probe (identified by
// the scan script's "find ." substring) with a canned
// "count=N size=N hash=H" line; every other script (mkdir, mv, git
// clone) just succeeds.
type recordingExec struct {
	scripts    []string
	guestCount int64
	guestSize  int64
	guestHash  string
}

func (r *recordingExec) exec() GuestExec {
	return func(script string) (string, string, int, error) {
		r.scripts = append(r.scripts, script)
		if strings.Contains(script, "find .") {
			return fmt.Sprintf("count=%d size=%d hash=%s\n", r.guestCount, r.guestSize, r.guestHash), "", 0, nil
		}
		return "", "", 0, nil
	}
}

func (r *recordingExec) containsCall(substr string) bool {
	for _, s := range r.scripts {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

// withFakeMirrorDir points the mirrorDirFn seam at root/<projectID>/<label>
// instead of the real cfg.RuntimeDir(), and restores the original on
// test cleanup.
func withFakeMirrorDir(t *testing.T, root string) {
	t.Helper()
	orig := mirrorDirFn
	mirrorDirFn = func(cfg identity.Config, projectID, label string) (string, bool, error) {
		p := filepath.Join(root, projectID, label)
		if err := os.MkdirAll(p, 0700); err != nil {
			return "", false, err
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return "", false, err
		}
		return p, len(entries) == 0, nil
	}
	t.Cleanup(func() { mirrorDirFn = orig })
}

// testApplyIdentity returns an identity.Config scoped to a scratch
// HOME, matching serviceapi/mutagen_sessions_test.go's
// testSessionsIdentity: mutagenSessionsDir/ConfigFilePath resolve off
// identity.Config.RuntimeDir(), which reads the real $HOME — without
// this, a passing test would write mutagen session config yaml under
// the developer's actual home directory.
func testApplyIdentity(t *testing.T) identity.Config {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	return identity.Config{Name: "devm-test", TLD: "test"}
}

// ---------- tests ----------

func TestApplyMutagenSessionChange_VolumeAdd_CreatesSession(t *testing.T) {
	withFakeMirrorDir(t, t.TempDir())
	cli := &fakeMutagenCLI{}
	rec := &recordingExec{}

	change := Change{
		Kind: KindVolumeChange, Op: OpAdd, Key: "pg-data",
		NewValue: schema.Volume{Path: "/var/lib/postgresql/data"},
	}
	err := applyMutagenSessionChange(context.Background(), rec.exec(), cli.build(), testApplyIdentity(t), "myproj", "", change)
	require.NoError(t, err)

	require.Len(t, cli.createArgs, 1)
	args := cli.createArgs[0]
	assert.Contains(t, args, "devm-myproj-data")
	assert.Contains(t, args, "devm@devm-myproj:/var/lib/postgresql/data")
	assert.True(t, rec.containsCall("mkdir -p '/var/lib/postgresql/data'"), "must ensure the guest dir exists")
}

func TestApplyMutagenSessionChange_VolumeRemove_TerminatesSession(t *testing.T) {
	withFakeMirrorDir(t, t.TempDir())
	cli := &fakeMutagenCLI{listSessions: []mutagen.SyncSession{
		{ID: "s1", Name: "devm-myproj-data", Status: "watching"},
	}}
	rec := &recordingExec{}

	change := Change{
		Kind: KindVolumeChange, Op: OpRemove, Key: "pg-data",
		OldValue: schema.Volume{Path: "/var/lib/postgresql/data"},
	}
	err := applyMutagenSessionChange(context.Background(), rec.exec(), cli.build(), testApplyIdentity(t), "myproj", "", change)
	require.NoError(t, err)

	assert.Equal(t, []string{"s1"}, cli.flushCalls)
	assert.Equal(t, []string{"s1"}, cli.terminateCalls)
	require.Empty(t, cli.createArgs, "remove must not create a new session")
}

func TestApplyMutagenSessionChange_LabelRename_MovesDirsAndRecreates(t *testing.T) {
	root := t.TempDir()
	withFakeMirrorDir(t, root)
	cli := &fakeMutagenCLI{listSessions: []mutagen.SyncSession{
		{ID: "s1", Name: "devm-myproj-oldname", Status: "watching"},
	}}
	rec := &recordingExec{}

	// Pre-seed the old mirror dir with a marker file so the rename's
	// os.Rename has real content to carry over.
	oldMirror := filepath.Join(root, "myproj", "oldname")
	require.NoError(t, os.MkdirAll(oldMirror, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(oldMirror, "marker.txt"), []byte("hi"), 0o644))

	before := schema.Volume{Path: "/data", Label: strPtr("oldname")}
	after := schema.Volume{Path: "/data", Label: strPtr("newname")}
	change := Change{
		Kind: KindVolumeChange, Op: OpMutate, Key: "data", Field: "label",
		VolumeBefore: &before, VolumeAfter: &after,
	}
	err := applyMutagenSessionChange(context.Background(), rec.exec(), cli.build(), testApplyIdentity(t), "myproj", "", change)
	require.NoError(t, err)

	assert.Equal(t, []string{"s1"}, cli.flushCalls, "old session must be flushed")
	assert.Equal(t, []string{"s1"}, cli.terminateCalls, "old session must be terminated")

	newMirror := filepath.Join(root, "myproj", "newname")
	_, statErr := os.Stat(filepath.Join(newMirror, "marker.txt"))
	require.NoError(t, statErr, "marker file must have moved with the mirror dir")
	_, oldStatErr := os.Stat(oldMirror)
	assert.True(t, os.IsNotExist(oldStatErr), "old mirror dir must no longer exist")

	require.Len(t, cli.createArgs, 1)
	assert.Contains(t, cli.createArgs[0], "devm-myproj-newname")
}

func TestApplyMutagenSessionChange_IgnoreChange_RegeneratesConfigAndRecreates(t *testing.T) {
	withFakeMirrorDir(t, t.TempDir())
	cli := &fakeMutagenCLI{listSessions: []mutagen.SyncSession{
		{ID: "s1", Name: "devm-myproj-data", Status: "watching"},
	}}
	rec := &recordingExec{}

	before := schema.Volume{Path: "/data", Ignore: []string{"*.log"}}
	after := schema.Volume{Path: "/data", Ignore: []string{"*.log", "*.tmp"}}
	change := Change{
		Kind: KindVolumeChange, Op: OpMutate, Key: "data", Field: "ignore",
		VolumeBefore: &before, VolumeAfter: &after,
	}
	err := applyMutagenSessionChange(context.Background(), rec.exec(), cli.build(), testApplyIdentity(t), "myproj", "", change)
	require.NoError(t, err)

	assert.Equal(t, []string{"s1"}, cli.flushCalls)
	assert.Equal(t, []string{"s1"}, cli.terminateCalls)
	require.Len(t, cli.createArgs, 1, "must recreate the session to pick up the new ignore list")
	assert.Contains(t, cli.createArgs[0], "devm-myproj-data")
}

func TestApplyMutagenSessionChange_RepoVolumeToggle_TrueToFalse(t *testing.T) {
	root := t.TempDir()
	withFakeMirrorDir(t, root)
	cli := &fakeMutagenCLI{listSessions: []mutagen.SyncSession{
		{ID: "s1", Name: "devm-myproj-extra", Status: "watching"},
	}}
	rec := &recordingExec{}

	mirror := filepath.Join(root, "myproj", "extra")
	require.NoError(t, os.MkdirAll(mirror, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(mirror, "f.txt"), []byte("x"), 0o644))

	before := schema.RepoConfig{URL: strPtr("git@example.com:a/extra.git"), Secret: "gh", Volume: boolPtr(true)}
	after := schema.RepoConfig{URL: strPtr("git@example.com:a/extra.git"), Secret: "gh", Volume: boolPtr(false)}
	change := Change{
		Kind: KindRepoChange, Op: OpMutate, Key: "extra", Field: "Volume",
		RepoBefore: &before, RepoAfter: &after,
	}
	err := applyMutagenSessionChange(context.Background(), rec.exec(), cli.build(), testApplyIdentity(t), "myproj", "", change)
	require.NoError(t, err)

	assert.Equal(t, []string{"s1"}, cli.flushCalls)
	assert.Equal(t, []string{"s1"}, cli.terminateCalls)
	require.Empty(t, cli.createArgs, "turning volume off must not create a session")

	_, statErr := os.Stat(mirror)
	assert.True(t, os.IsNotExist(statErr), "mac mirror dir must be removed")
}

func TestApplyMutagenSessionChange_RepoVolumeToggle_FalseToTrue(t *testing.T) {
	withFakeMirrorDir(t, t.TempDir())
	cli := &fakeMutagenCLI{}
	rec := &recordingExec{} // guest reports empty on scan -> guard trivially passes

	before := schema.RepoConfig{URL: strPtr("git@example.com:a/extra.git"), Secret: "gh", Volume: boolPtr(false)}
	after := schema.RepoConfig{URL: strPtr("git@example.com:a/extra.git"), Secret: "gh", Volume: boolPtr(true)}
	change := Change{
		Kind: KindRepoChange, Op: OpMutate, Key: "extra", Field: "Volume",
		RepoBefore: &before, RepoAfter: &after,
	}
	err := applyMutagenSessionChange(context.Background(), rec.exec(), cli.build(), testApplyIdentity(t), "myproj", "http://127.0.0.1:5555", change)
	require.NoError(t, err)

	require.Len(t, cli.createArgs, 1)
	assert.Contains(t, cli.createArgs[0], "devm-myproj-extra")
	assert.Contains(t, cli.createArgs[0], "devm@devm-myproj:/home/devm/extra")
}

func TestApplyMutagenSessionChange_PrimaryToggle_NoOp(t *testing.T) {
	withFakeMirrorDir(t, t.TempDir())
	cli := &fakeMutagenCLI{}
	rec := &recordingExec{}

	before := schema.RepoConfig{URL: strPtr("git@example.com:a/extra.git"), Secret: "gh", Primary: boolPtr(false)}
	after := schema.RepoConfig{URL: strPtr("git@example.com:a/extra.git"), Secret: "gh", Primary: boolPtr(true)}
	change := Change{
		Kind: KindRepoChange, Op: OpMutate, Key: "extra", Field: "Primary",
		RepoBefore: &before, RepoAfter: &after,
	}
	err := applyMutagenSessionChange(context.Background(), rec.exec(), cli.build(), testApplyIdentity(t), "myproj", "", change)
	require.NoError(t, err)

	assert.Empty(t, cli.createArgs)
	assert.Empty(t, cli.flushCalls)
	assert.Empty(t, cli.terminateCalls)
	assert.Empty(t, rec.scripts, "a primary toggle must not touch the guest at all")
}

func TestApplyMutagenSessionChange_VolumePathMutate_MovesGuestDir(t *testing.T) {
	withFakeMirrorDir(t, t.TempDir())
	cli := &fakeMutagenCLI{listSessions: []mutagen.SyncSession{
		{ID: "s1", Name: "devm-myproj-data", Status: "watching"},
	}}
	rec := &recordingExec{}

	before := schema.Volume{Path: "/old-data", Label: strPtr("data")}
	after := schema.Volume{Path: "/new-data", Label: strPtr("data")}
	change := Change{
		Kind: KindVolumeChange, Op: OpMutate, Key: "data", Field: "path",
		VolumeBefore: &before, VolumeAfter: &after,
	}
	err := applyMutagenSessionChange(context.Background(), rec.exec(), cli.build(), testApplyIdentity(t), "myproj", "", change)
	require.NoError(t, err)

	assert.Equal(t, []string{"s1"}, cli.flushCalls, "old session must be flushed before the guest dir moves")
	assert.Equal(t, []string{"s1"}, cli.terminateCalls, "old session must be terminated before the guest dir moves")
	assert.True(t, rec.containsCall("mv '/old-data' '/new-data'"), "must mv the guest dir to its new path")

	require.Len(t, cli.createArgs, 1)
	assert.Contains(t, cli.createArgs[0], "devm-myproj-data")
	assert.Contains(t, cli.createArgs[0], "devm@devm-myproj:/new-data")
}

func TestApplyMutagenSessionChange_RepoLabelRename_MovesGuestCloneDirAndRecreates(t *testing.T) {
	root := t.TempDir()
	withFakeMirrorDir(t, root)
	cli := &fakeMutagenCLI{listSessions: []mutagen.SyncSession{
		{ID: "s1", Name: "devm-myproj-oldname", Status: "watching"},
	}}
	rec := &recordingExec{}

	// Pre-seed the old mirror dir with a marker file so the rename's
	// os.Rename has real content to carry over.
	oldMirror := filepath.Join(root, "myproj", "oldname")
	require.NoError(t, os.MkdirAll(oldMirror, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(oldMirror, "marker.txt"), []byte("hi"), 0o644))

	before := schema.RepoConfig{URL: strPtr("git@example.com:a/repo.git"), Secret: "gh", Volume: boolPtr(true), Label: strPtr("oldname")}
	after := schema.RepoConfig{URL: strPtr("git@example.com:a/repo.git"), Secret: "gh", Volume: boolPtr(true), Label: strPtr("newname")}
	change := Change{
		Kind: KindRepoChange, Op: OpMutate, Key: "extra", Field: "Label",
		RepoBefore: &before, RepoAfter: &after,
	}
	err := applyMutagenSessionChange(context.Background(), rec.exec(), cli.build(), testApplyIdentity(t), "myproj", "", change)
	require.NoError(t, err)

	assert.Equal(t, []string{"s1"}, cli.flushCalls)
	assert.Equal(t, []string{"s1"}, cli.terminateCalls)
	assert.True(t, rec.containsCall("mv '/home/devm/oldname' '/home/devm/newname'"),
		"must mv the guest clone dir — unlike a volume, a repo's guest path is label-derived")

	newMirror := filepath.Join(root, "myproj", "newname")
	_, statErr := os.Stat(filepath.Join(newMirror, "marker.txt"))
	require.NoError(t, statErr, "marker file must have moved with the mac mirror dir")
	_, oldStatErr := os.Stat(oldMirror)
	assert.True(t, os.IsNotExist(oldStatErr), "old mac mirror dir must no longer exist")

	require.Len(t, cli.createArgs, 1)
	assert.Contains(t, cli.createArgs[0], "devm-myproj-newname")
	assert.Contains(t, cli.createArgs[0], "devm@devm-myproj:/home/devm/newname")
}

func TestApplyMutagenSessionChange_RepoIgnoreChange_RegeneratesConfigAndRecreates(t *testing.T) {
	withFakeMirrorDir(t, t.TempDir())
	cli := &fakeMutagenCLI{listSessions: []mutagen.SyncSession{
		{ID: "s1", Name: "devm-myproj-myrepo", Status: "watching"},
	}}
	// A non-empty, non-zero guest scan simulates an already-cloned repo
	// so an ignore-only change doesn't spuriously re-clone.
	rec := &recordingExec{guestCount: 5, guestSize: 500, guestHash: "abc"}

	before := schema.RepoConfig{URL: strPtr("git@example.com:a/myrepo.git"), Secret: "gh", Volume: boolPtr(true), Ignore: []string{"*.log"}}
	after := schema.RepoConfig{URL: strPtr("git@example.com:a/myrepo.git"), Secret: "gh", Volume: boolPtr(true), Ignore: []string{"*.log", "*.tmp"}}
	change := Change{
		Kind: KindRepoChange, Op: OpMutate, Key: "extra", Field: "Ignore",
		RepoBefore: &before, RepoAfter: &after,
	}
	err := applyMutagenSessionChange(context.Background(), rec.exec(), cli.build(), testApplyIdentity(t), "myproj", "http://127.0.0.1:5555", change)
	require.NoError(t, err)

	assert.Equal(t, []string{"s1"}, cli.flushCalls)
	assert.Equal(t, []string{"s1"}, cli.terminateCalls)
	assert.False(t, rec.containsCall("git clone"), "ignore-only change must not re-clone an already-populated repo")
	require.Len(t, cli.createArgs, 1, "must recreate the session to pick up the new ignore list")
	assert.Contains(t, cli.createArgs[0], "devm-myproj-myrepo")
	assert.Contains(t, cli.createArgs[0], "devm@devm-myproj:/home/devm/myrepo")
}

func TestApplyMutagenSessionChange_RepoOpRemove_TerminatesAndClearsMirror(t *testing.T) {
	root := t.TempDir()
	withFakeMirrorDir(t, root)
	cli := &fakeMutagenCLI{listSessions: []mutagen.SyncSession{
		{ID: "s1", Name: "devm-myproj-extra", Status: "watching"},
	}}
	rec := &recordingExec{}

	// Empty mirror dir (no un-synced content) — must be cleaned up.
	mirror := filepath.Join(root, "myproj", "extra")
	require.NoError(t, os.MkdirAll(mirror, 0700))

	change := Change{
		Kind: KindRepoChange, Op: OpRemove, Key: "extra",
		OldValue: schema.RepoConfig{URL: strPtr("git@example.com:a/extra.git"), Secret: "gh", Volume: boolPtr(true)},
	}
	err := applyMutagenSessionChange(context.Background(), rec.exec(), cli.build(), testApplyIdentity(t), "myproj", "", change)
	require.NoError(t, err)

	assert.Equal(t, []string{"s1"}, cli.flushCalls)
	assert.Equal(t, []string{"s1"}, cli.terminateCalls)
	require.Empty(t, cli.createArgs, "remove must never create a session")

	_, statErr := os.Stat(mirror)
	assert.True(t, os.IsNotExist(statErr), "empty mac mirror dir must be cleaned up on remove")
}

func TestApplyMutagenSessionChange_RepoOpAdd_VolumeFalse_JustClones(t *testing.T) {
	withFakeMirrorDir(t, t.TempDir())
	cli := &fakeMutagenCLI{}
	rec := &recordingExec{} // guest empty -> clone must run

	change := Change{
		Kind: KindRepoChange, Op: OpAdd, Key: "extra",
		NewValue: schema.RepoConfig{URL: strPtr("git@example.com:a/extra.git"), Secret: "gh", Volume: boolPtr(false)},
	}
	err := applyMutagenSessionChange(context.Background(), rec.exec(), cli.build(), testApplyIdentity(t), "myproj", "http://127.0.0.1:5555", change)
	require.NoError(t, err)

	assert.Empty(t, cli.createArgs, "volume:false must never create a mutagen session")
	assert.True(t, rec.containsCall("git clone"), "must cold-start clone the repo into the guest")
	assert.True(t, rec.containsCall("/home/devm/extra"), "must clone at the label-derived guest path")
}
