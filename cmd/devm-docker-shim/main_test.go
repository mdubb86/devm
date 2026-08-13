package main

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubVerify installs a builder-verify stub that always reports OK,
// so transformArgv tests exercise the rewrite path without shelling
// out to docker.
func stubVerify(t *testing.T) {
	t.Helper()
	orig := verifyBuilderHealthyFn
	verifyBuilderHealthyFn = func() error { return nil }
	t.Cleanup(func() { verifyBuilderHealthyFn = orig })
}

func stubVerifyFail(t *testing.T, err error) {
	t.Helper()
	orig := verifyBuilderHealthyFn
	verifyBuilderHealthyFn = func() error { return err }
	t.Cleanup(func() { verifyBuilderHealthyFn = orig })
}

func TestTransformArgv_BuildRewritesToBuildxWithDevmBuilder(t *testing.T) {
	stubVerify(t)
	// Bare `docker build` implies "load into local image store" — the
	// devm builder's `remote` driver doesn't do that by default, so
	// the shim auto-adds --load. See loadNeeded in main.go.
	got, err := transformArgv([]string{"build", "-t", "myimg", "."})
	require.NoError(t, err)
	assert.Equal(t,
		[]string{"buildx", "build", "--builder", "devm", "--load", "-t", "myimg", "."},
		got,
	)
}

func TestTransformArgv_BuildWithGlobalFlagsPreserved(t *testing.T) {
	stubVerify(t)
	// --context myctx is a global docker flag (takes a value); the
	// rewrite must keep it before the subcommand.
	got, err := transformArgv([]string{"--context", "myctx", "build", "-t", "myimg", "."})
	require.NoError(t, err)
	assert.Equal(t,
		[]string{"--context", "myctx", "buildx", "build", "--builder", "devm", "--load", "-t", "myimg", "."},
		got,
	)
}

// TestTransformArgv_BuildLoadNotDoubleAdded pins: if the user already
// passed --load, the shim's auto-inject must not duplicate it (docker
// tolerates duplicate --load today, but adding a redundant flag
// silently is exactly the kind of noise we want to avoid).
func TestTransformArgv_BuildLoadNotDoubleAdded(t *testing.T) {
	stubVerify(t)
	got, err := transformArgv([]string{"build", "--load", "-t", "myimg", "."})
	require.NoError(t, err)
	assert.Equal(t,
		[]string{"buildx", "build", "--builder", "devm", "--load", "-t", "myimg", "."},
		got,
	)
}

// TestTransformArgv_BuildPushNoLoad pins: --push means "the built image
// goes to a registry, not the local store" — auto-adding --load would
// conflict (docker errors: "push and load may not be set together").
func TestTransformArgv_BuildPushNoLoad(t *testing.T) {
	stubVerify(t)
	got, err := transformArgv([]string{"build", "--push", "-t", "myrepo/myimg:latest", "."})
	require.NoError(t, err)
	assert.Equal(t,
		[]string{"buildx", "build", "--builder", "devm", "--push", "-t", "myrepo/myimg:latest", "."},
		got,
	)
}

// TestTransformArgv_BuildOutputNoLoad pins: -o / --output = user picked
// where the build result goes (tarball, filesystem export, oci layout);
// --load would fight that.
func TestTransformArgv_BuildOutputNoLoad(t *testing.T) {
	stubVerify(t)
	cases := []struct {
		name string
		argv []string
	}{
		{"-o short", []string{"build", "-o", "type=local,dest=./out", "."}},
		{"--output long", []string{"build", "--output", "type=tar,dest=out.tar", "."}},
		{"--output= equals form", []string{"build", "--output=type=oci,dest=out.oci", "."}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := transformArgv(tc.argv)
			require.NoError(t, err)
			for _, a := range got {
				assert.NotEqual(t, "--load", a, "auto-load must be skipped when the user set an output")
			}
		})
	}
}

// TestTransformArgv_BuildxBuildDoesNotAutoLoad pins: direct `docker
// buildx build` invocations are the user explicitly choosing buildx.
// They manage their own output flags — the shim only injects --builder.
func TestTransformArgv_BuildxBuildDoesNotAutoLoad(t *testing.T) {
	stubVerify(t)
	got, err := transformArgv([]string{"buildx", "build", "-t", "myimg", "."})
	require.NoError(t, err)
	for _, a := range got {
		assert.NotEqual(t, "--load", a, "buildx build path must not auto-add --load")
	}
	assert.Equal(t,
		[]string{"buildx", "build", "--builder", "devm", "-t", "myimg", "."},
		got,
	)
}

// TestLoadNeeded_ArityAware pins: an output-flag-shaped token consumed
// as the value of a preceding valued flag (e.g. `-t --load` = tag
// literally named "--load") must NOT be treated as a real --load, or
// the shim would skip auto-injecting when it shouldn't.
func TestLoadNeeded_ArityAware(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"real --load", []string{"--load", "."}, false},
		{"real --push", []string{"--push", "-t", "img", "."}, false},
		{"real -o", []string{"-o", "type=tar,dest=out.tar", "."}, false},
		{"real --output", []string{"--output", "type=local", "."}, false},
		{"--output= equals", []string{"--output=type=oci", "."}, false},
		{"--load as -t's value (contrived)", []string{"-t", "--load", "."}, true},
		{"--push as -f's value (contrived)", []string{"-f", "--push", "."}, true},
		{"nothing that specifies output", []string{"-t", "img", "."}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := loadNeeded(tc.args)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestTransformArgv_BuildxBuildInjectsBuilder(t *testing.T) {
	stubVerify(t)
	got, err := transformArgv([]string{"buildx", "build", "-t", "myimg", "."})
	require.NoError(t, err)
	assert.Equal(t,
		[]string{"buildx", "build", "--builder", "devm", "-t", "myimg", "."},
		got,
	)
}

func TestTransformArgv_BuildxBuildRespectsUserBuilder(t *testing.T) {
	// No verify stub: this path must NOT call verifyBuilderHealthy at all.
	verifyBuilderHealthyFn = func() error {
		t.Fatal("verifyBuilderHealthy called despite user --builder")
		return nil
	}
	t.Cleanup(func() { verifyBuilderHealthyFn = realVerifyBuilderHealthy })

	got, err := transformArgv([]string{"buildx", "build", "--builder", "mine", "-t", "img", "."})
	require.NoError(t, err)
	assert.Equal(t,
		[]string{"buildx", "build", "--builder", "mine", "-t", "img", "."},
		got,
	)
}

func TestTransformArgv_BuildxBuildRespectsUserBuilderEqualsForm(t *testing.T) {
	verifyBuilderHealthyFn = func() error {
		t.Fatal("verifyBuilderHealthy called despite user --builder=")
		return nil
	}
	t.Cleanup(func() { verifyBuilderHealthyFn = realVerifyBuilderHealthy })

	got, err := transformArgv([]string{"buildx", "build", "--builder=mine", "-t", "img", "."})
	require.NoError(t, err)
	assert.Equal(t,
		[]string{"buildx", "build", "--builder=mine", "-t", "img", "."},
		got,
	)
}

// TestUserSetBuilder_ArityAware pins the arity-tracking behavior:
// a `--builder` token that is CONSUMED as the value of a preceding
// valued flag (e.g. `-f --builder .` where -f=Dockerfile-named-"--builder")
// must NOT trigger userSetBuilder — otherwise the shim would skip
// injection and route to embedded BuildKit, breaking transparent build.
func TestUserSetBuilder_ArityAware(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want bool
	}{
		{"real user --builder", []string{"buildx", "build", "--builder", "mine", "."}, true},
		{"real user --builder= form", []string{"buildx", "build", "--builder=mine", "."}, true},
		{"--builder as -f's value (contrived)", []string{"buildx", "build", "-f", "--builder", "."}, false},
		{"--builder as --file's value", []string{"buildx", "build", "--file", "--builder", "."}, false},
		{"--builder as -t's value", []string{"buildx", "build", "-t", "--builder", "."}, false},
		{"real --builder after -f Dockerfile", []string{"buildx", "build", "-f", "Dockerfile", "--builder", "mine", "."}, true},
		{"real --builder after boolean flag", []string{"buildx", "build", "--no-cache", "--builder", "mine", "."}, true},
		{"docker global --context then --builder", []string{"--context", "myctx", "buildx", "build", "--builder", "mine", "."}, true},
		{"--builder as --context's value (contrived)", []string{"--context", "--builder", "buildx", "build", "."}, false},
		{"no --builder anywhere", []string{"buildx", "build", "-t", "img", "."}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := userSetBuilder(tc.argv)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestTransformArgv_BuildxBuildFValueBuilderInjectsCorrectly is the
// end-to-end sibling of TestUserSetBuilder_ArityAware: -f --builder
// should NOT prevent devm from injecting its own --builder devm.
func TestTransformArgv_BuildxBuildFValueBuilderInjectsCorrectly(t *testing.T) {
	stubVerify(t)
	got, err := transformArgv([]string{"buildx", "build", "-f", "--builder", "."})
	require.NoError(t, err)
	assert.Equal(t,
		[]string{"buildx", "build", "--builder", "devm", "-f", "--builder", "."},
		got,
	)
}

func TestTransformArgv_RunPassesThroughUnchanged(t *testing.T) {
	verifyBuilderHealthyFn = func() error {
		t.Fatal("verifyBuilderHealthy called for docker run")
		return nil
	}
	t.Cleanup(func() { verifyBuilderHealthyFn = realVerifyBuilderHealthy })

	got, err := transformArgv([]string{"run", "-it", "img"})
	require.NoError(t, err)
	assert.Equal(t, []string{"run", "-it", "img"}, got)
}

func TestTransformArgv_BuildxInspectPassesThroughUnchanged(t *testing.T) {
	// buildx subcommand that isn't `build` — must not touch it.
	verifyBuilderHealthyFn = func() error {
		t.Fatal("verifyBuilderHealthy called for buildx inspect")
		return nil
	}
	t.Cleanup(func() { verifyBuilderHealthyFn = realVerifyBuilderHealthy })

	got, err := transformArgv([]string{"buildx", "inspect", "devm"})
	require.NoError(t, err)
	assert.Equal(t, []string{"buildx", "inspect", "devm"}, got)
}

func TestTransformArgv_UnhealthyBuilderErrorsOnBuild(t *testing.T) {
	stubVerifyFail(t, fmt.Errorf("boom"))
	_, err := transformArgv([]string{"build", "."})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

func TestTransformArgv_UnhealthyBuilderErrorsOnBuildxBuild(t *testing.T) {
	stubVerifyFail(t, fmt.Errorf("boom"))
	_, err := transformArgv([]string{"buildx", "build", "."})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

func TestFirstPositional_SkipsValuedGlobalFlags(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{"bare", []string{"build"}, "build"},
		{"context-space", []string{"--context", "x", "build"}, "build"},
		{"context-equals", []string{"--context=x", "build"}, "build"},
		{"host-space", []string{"--host", "tcp://y", "run"}, "run"},
		{"log-level-short", []string{"-l", "debug", "build"}, "build"},
		{"no-positional", []string{"--context", "x"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, _ := firstPositional(tc.argv)
			assert.Equal(t, tc.want, got)
		})
	}
}
