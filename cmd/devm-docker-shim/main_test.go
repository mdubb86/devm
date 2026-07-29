package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShouldInjectSecret(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want bool
	}{
		{"plain_build", []string{"build", "."}, true},
		{"plain_build_with_flags", []string{"build", "-t", "foo:latest", "."}, true},
		{"buildx_build", []string{"buildx", "build", "."}, true},
		{"buildx_build_with_flags", []string{"buildx", "build", "--platform", "linux/arm64", "."}, true},
		{"global_flag_then_build", []string{"--context", "default", "build", "."}, true},
		{"global_short_flag_then_build", []string{"-H", "unix:///var/run/docker.sock", "build", "."}, true},
		{"equals_form_flag_then_build", []string{"--log-level=debug", "build", "."}, true},
		{"global_flag_then_buildx_build", []string{"--context", "default", "buildx", "build", "."}, true},
		{"run_no_inject", []string{"run", "alpine", "sh"}, false},
		{"pull_no_inject", []string{"pull", "alpine"}, false},
		{"version_no_inject", []string{"version"}, false},
		{"info_no_inject", []string{"info"}, false},
		{"buildx_bake_no_inject", []string{"buildx", "bake"}, false},
		{"buildx_ls_no_inject", []string{"buildx", "ls"}, false},
		{"empty_no_inject", []string{}, false},
		{"only_global_flags_no_inject", []string{"--debug"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldInjectSecret(tc.argv); got != tc.want {
				t.Errorf("shouldInjectSecret(%v) = %v, want %v", tc.argv, got, tc.want)
			}
		})
	}
}

// TestRun_InjectsContainerEnvForRunCreateExec proves that docker run/create/exec
// invocations get -e KEY=VAL args prepended right after the subcommand,
// drawing values from /etc/environment. Uses a synthetic /etc/env body
// via test seam.
func TestRun_InjectsContainerEnvForRunCreateExec(t *testing.T) {
	orig := etcEnvironmentReader
	t.Cleanup(func() { etcEnvironmentReader = orig })
	etcEnvironmentReader = func() (string, error) {
		return "NODE_EXTRA_CA_CERTS=/x\nUV_SYSTEM_CERTS=1\n", nil
	}

	cases := []struct {
		name string
		argv []string
	}{
		{name: "run", argv: []string{"run", "-it", "nginx"}},
		{name: "create", argv: []string{"create", "--name", "foo", "nginx"}},
		{name: "exec", argv: []string{"exec", "-it", "container", "bash"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := transformArgv(tc.argv)
			// Position: our -e args come RIGHT AFTER the subcommand,
			// before positional (image / container / cmd).
			require.GreaterOrEqual(t, len(got), 5)
			assert.Equal(t, tc.argv[0], got[0], "subcommand at position 0")
			// Look for our injected -e entries anywhere between position 1 and
			// the end of the injected block.
			joined := strings.Join(got, " ")
			assert.Contains(t, joined, "-e NODE_EXTRA_CA_CERTS=/x")
			assert.Contains(t, joined, "-e UV_SYSTEM_CERTS=1")
			// Original tail preserved.
			for _, orig := range tc.argv[1:] {
				assert.Contains(t, got, orig)
			}
		})
	}
}

func TestRun_DoesNotInjectForNonContainerSubcommands(t *testing.T) {
	orig := etcEnvironmentReader
	t.Cleanup(func() { etcEnvironmentReader = orig })
	etcEnvironmentReader = func() (string, error) {
		return "NODE_EXTRA_CA_CERTS=/x\n", nil
	}

	for _, cmd := range []string{"ps", "images", "logs", "pull"} {
		t.Run(cmd, func(t *testing.T) {
			argv := []string{cmd}
			got := transformArgv(argv)
			assert.Equal(t, argv, got, "argv should be unchanged for %q", cmd)
		})
	}
}

func TestFirstPositional(t *testing.T) {
	cases := []struct {
		name  string
		argv  []string
		want  string
		rest  []string
		found bool
	}{
		{"empty", []string{}, "", nil, false},
		{"only_flag", []string{"--debug"}, "", nil, false},
		{"single_positional", []string{"info"}, "info", []string{}, true},
		{"flag_then_positional", []string{"--debug", "info"}, "info", []string{}, true},
		{"valued_flag_skipped", []string{"--context", "default", "build"}, "build", []string{}, true},
		{"equals_flag_not_valued", []string{"--log-level=info", "build"}, "build", []string{}, true},
		{
			"two_positionals_returns_first",
			[]string{"buildx", "build", "."},
			"buildx",
			[]string{"build", "."},
			true,
		},
		{
			"short_valued_flag_skipped",
			[]string{"-H", "unix:///var/run/docker.sock", "ps"},
			"ps",
			[]string{},
			true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, rest, found := firstPositional(tc.argv)
			if got != tc.want || found != tc.found || !reflect.DeepEqual(rest, tc.rest) {
				t.Errorf("firstPositional(%v) = (%q, %v, %v), want (%q, %v, %v)",
					tc.argv, got, rest, found, tc.want, tc.rest, tc.found)
			}
		})
	}
}

// TestRun_InjectsAfterSubcommand_HonorsGlobalValuedFlags proves the
// insertion respects docker's valued global flags (--context, --host,
// --config, --log-level, and their short forms). Without this, `docker
// --context foo run ...` would get `-e` inserted BEFORE `run`, where
// it's an invalid docker global flag and docker rejects the whole
// command.
func TestRun_InjectsAfterSubcommand_HonorsGlobalValuedFlags(t *testing.T) {
	orig := etcEnvironmentReader
	t.Cleanup(func() { etcEnvironmentReader = orig })
	etcEnvironmentReader = func() (string, error) {
		return "NODE_EXTRA_CA_CERTS=/x\n", nil
	}

	cases := []struct {
		name string
		argv []string
	}{
		{name: "--context", argv: []string{"--context", "myctx", "run", "-it", "nginx"}},
		{name: "--host", argv: []string{"--host", "tcp://localhost:2375", "run", "nginx"}},
		{name: "-H short", argv: []string{"-H", "tcp://localhost:2375", "run", "nginx"}},
		{name: "-c short", argv: []string{"-c", "myctx", "run", "nginx"}},
		{name: "--log-level", argv: []string{"--log-level", "debug", "run", "nginx"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := transformArgv(tc.argv)
			// Find "run" position in output.
			runIdx := -1
			for i, a := range got {
				if a == "run" {
					runIdx = i
					break
				}
			}
			require.NotEqual(t, -1, runIdx, "expected 'run' in output %v", got)
			// -e must come AFTER "run", not before.
			for i, a := range got {
				if a == "-e" {
					assert.Greater(t, i, runIdx,
						"'-e' at position %d must come AFTER 'run' at position %d; got argv %v",
						i, runIdx, got)
				}
			}
		})
	}
}
