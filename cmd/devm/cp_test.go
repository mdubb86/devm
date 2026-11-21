package main

import (
	"path/filepath"
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCpArg(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    cpArg
		wantErr string
	}{
		{"colon-only", ":/etc/foo", cpArg{Remote: true, Path: "/etc/foo"}, ""},
		{"colon-only-nested", ":/var/lib/x/y.log", cpArg{Remote: true, Path: "/var/lib/x/y.log"}, ""},
		{"project prefix", "buzztrack:/root/dump.sql", cpArg{Remote: true, Project: "buzztrack", Path: "/root/dump.sql"}, ""},
		{"local absolute", "/tmp/foo.txt", cpArg{Path: "/tmp/foo.txt"}, ""},
		{"local relative", "foo.txt", cpArg{Path: "foo.txt"}, ""},
		{"local with dot slash", "./foo.txt", cpArg{Path: "./foo.txt"}, ""},
		{"local with colon disambiguated", "./foo:bar", cpArg{Path: "./foo:bar"}, ""},
		{"stdin/stdout sentinel", "-", cpArg{Path: "-"}, ""},
		{"empty", "", cpArg{}, "empty path"},
		{"colon-only-empty", ":", cpArg{}, `":" prefix requires`},
		{"colon-only-relative", ":relative/path", cpArg{}, "guest path must be absolute"},
		{"project prefix relative", "proj:relative/path", cpArg{}, "guest path must be absolute"},
		{"project prefix empty path", "proj:", cpArg{}, `missing a path after ":"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCpArg(tc.in)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestResolveDirection(t *testing.T) {
	local := cpArg{Path: "foo.txt"}
	remote := cpArg{Remote: true, Path: "/etc/foo"}
	remoteProj := cpArg{Remote: true, Project: "buzz", Path: "/etc/foo"}

	cases := []struct {
		name    string
		src     cpArg
		dst     cpArg
		want    direction
		wantErr string
	}{
		{"local → remote", local, remote, directionUpload, ""},
		{"remote → local", remote, local, directionDownload, ""},
		{"local → remote (proj)", local, remoteProj, directionUpload, ""},
		{"remote (proj) → local", remoteProj, local, directionDownload, ""},
		{"both remote", remote, remoteProj, 0, "does not support guest-to-guest"},
		{"both local", local, local, 0, "use plain `cp`"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveDirection(tc.src, tc.dst)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestMountPassthrough_NoRepo_WorkspaceRootNotMirrored(t *testing.T) {
	// Without `repo:`, the daemon never mounts a primary workspace volume
	// at all — a guest path under the Mac cwd is NOT mirrored anywhere,
	// so it must fall through to pipe transport rather than resolving to
	// a (nonexistent) storage dir.
	gotHost, gotOK := mountPassthrough(
		"/Users/me/workspace/foo/src/main.go", "/Users/me/workspace/foo",
		schema.Config{}, "myproj",
	)
	assert.False(t, gotOK)
	assert.Empty(t, gotHost)
}

func TestMountPassthrough_NoRepoRoot_NoConfig(t *testing.T) {
	// When project was named explicitly (no CWD walk), we don't have a
	// repoRoot or cfg. Every path must fall through to pipe transport.
	gotHost, gotOK := mountPassthrough("/Users/me/workspace/foo/src/main.go", "", schema.Config{}, "")
	assert.False(t, gotOK)
	assert.Empty(t, gotHost)
}

func TestMountPassthrough_ResolvesPrimaryRepoViaLabelTable(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate cfg.RuntimeDir() from the real HOME
	repoRoot := t.TempDir()
	url := "https://example.com/main.git"
	primary := true
	pcfg := schema.Config{
		Repos: map[string]schema.RepoConfig{
			"main": {URL: &url, Primary: &primary},
		},
	}

	gotHost, gotOK := mountPassthrough("/home/devm/main/src/main.go", repoRoot, pcfg, "myproj")
	require.True(t, gotOK)
	assert.Equal(t, filepath.Join(cfg.RuntimeDir(), "myproj", "main", "src", "main.go"), gotHost)
}

func TestMountPassthrough_PicksDeepestContainingEntry(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate cfg.RuntimeDir() from the real HOME
	repoRoot := t.TempDir()
	url := "https://example.com/main.git"
	primary := true
	pcfg := schema.Config{
		Repos: map[string]schema.RepoConfig{
			"main": {URL: &url, Primary: &primary},
		},
		Volumes: map[string]schema.Volume{
			"data": {Path: "/home/devm/main/data"},
		},
	}

	// "/home/devm/main/data/x.db" is contained by both the repo entry
	// ("/home/devm/main") and the nested volume entry
	// ("/home/devm/main/data") — the deeper (volume) entry must win.
	gotHost, gotOK := mountPassthrough("/home/devm/main/data/x.db", repoRoot, pcfg, "myproj")
	require.True(t, gotOK)
	assert.Equal(t, filepath.Join(cfg.RuntimeDir(), "myproj", "data", "x.db"), gotHost)
}

func TestMountPassthrough_NoMirrorSecondary_FallsBackToPipe(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate cfg.RuntimeDir() from the real HOME
	repoRoot := t.TempDir()
	mainURL := "https://example.com/main.git"
	primary := true
	dataURL := "https://example.com/data.git"
	pcfg := schema.Config{
		Repos: map[string]schema.RepoConfig{
			"main": {URL: &mainURL, Primary: &primary},
			"data": {URL: &dataURL}, // volume: unset -> NoMirror, no Mac mirror dir exists
		},
	}

	// A NoMirror secondary has no Mac-side mirror storage — cp must not
	// synthesize a passthrough path into a mirror dir that was never
	// created, and must instead report no known mount so the caller
	// falls back to pipe transport.
	gotHost, gotOK := mountPassthrough("/home/devm/data/src/main.go", repoRoot, pcfg, "myproj")
	assert.False(t, gotOK)
	assert.Empty(t, gotHost)
}

func TestMountPassthrough_PathOutsideAnyEntry(t *testing.T) {
	repoRoot := t.TempDir()
	url := "https://example.com/main.git"
	primary := true
	pcfg := schema.Config{
		Repos: map[string]schema.RepoConfig{
			"main": {URL: &url, Primary: &primary},
		},
	}

	gotHost, gotOK := mountPassthrough("/etc/passwd", repoRoot, pcfg, "myproj")
	assert.False(t, gotOK)
	assert.Empty(t, gotHost)
}

func TestInside(t *testing.T) {
	cases := []struct {
		name   string
		target string
		root   string
		want   bool
	}{
		{"exact match", "/a/b", "/a/b", true},
		{"child", "/a/b/c", "/a/b", true},
		{"grandchild", "/a/b/c/d", "/a/b", true},
		{"sibling with prefix", "/a/bc", "/a/b", false},
		{"parent", "/a", "/a/b", false},
		{"unrelated", "/x/y", "/a/b", false},
		{"trailing slash in root normalized", "/a/b/c", "/a/b/", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, inside(tc.target, tc.root))
		})
	}
}

func TestShellQuote_RoundTripsThroughShSpec(t *testing.T) {
	// Not an exhaustive shell parser — just pins the two shapes that
	// break naive quoters: paths with single quotes, and paths with
	// spaces. The command uses this to embed guest paths inside
	// `sh -c 'tee %s'`; anything that breaks out of the outer quoting
	// is a security problem.
	cases := []struct {
		in   string
		want string
	}{
		{"/etc/foo", `'/etc/foo'`},
		{"/tmp/with space", `'/tmp/with space'`},
		{"/tmp/with'quote", `'/tmp/with'\''quote'`},
		{"", `''`},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, shellQuote(tc.in))
		})
	}
}

func TestIsPermissionDenied(t *testing.T) {
	assert.False(t, isPermissionDenied(nil))
	assert.False(t, isPermissionDenied(&pipeError{stderr: "no such file"}))
	assert.True(t, isPermissionDenied(&pipeError{stderr: "tee: /etc/foo: Permission denied"}))
	assert.True(t, isPermissionDenied(&pipeError{stderr: "cat: /root/x: permission denied"}))
	assert.True(t, isPermissionDenied(&pipeError{stderr: "cp: cannot open '/root/x': Operation not permitted"}))
}
