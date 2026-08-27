package main

import (
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

func TestMountPassthrough(t *testing.T) {
	// Standard project layout: workspace at /Users/me/workspace/foo,
	// plus a user mount at /Users/me/data. mountPassthrough's repo-backed
	// primary-workspace branch is a TODO(Task 17) no-op for now (see
	// cmd/devm/cp.go), so only the mounts[]-based cases below exercise
	// live behavior — the interim reality is that a repo-backed
	// workspace path falls through to pipe transport just like
	// TestMountPassthrough_NoRepo_WorkspaceRootNotMirrored.
	repoRoot := "/Users/me/workspace/foo"
	projectName := "myproj"
	pcfg := schema.Config{
		Mounts: []string{
			"/Users/me/data",
			"/Users/me/read-only:ro",
		},
	}

	cases := []struct {
		name      string
		guestPath string
		wantHost  string
		wantOK    bool
	}{
		{"under user mount", "/Users/me/data/big.csv", "/Users/me/data/big.csv", true},
		{"under ro user mount", "/Users/me/read-only/setup.sql", "/Users/me/read-only/setup.sql", true},
		{"outside everything (/etc)", "/etc/hosts", "", false},
		{"outside everything (/var)", "/var/log/foo.log", "", false},
		{"prefix collision (not really under)", "/Users/me/workspace/foobar/x", "", false},
		{"prefix collision on mount", "/Users/me/dataother/x", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotHost, gotOK := mountPassthrough(tc.guestPath, repoRoot, pcfg, projectName)
			assert.Equal(t, tc.wantOK, gotOK)
			if tc.wantOK {
				assert.Equal(t, tc.wantHost, gotHost)
			}
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
