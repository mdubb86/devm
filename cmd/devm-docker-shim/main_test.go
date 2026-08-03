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
	got, err := transformArgv([]string{"build", "-t", "myimg", "."})
	require.NoError(t, err)
	assert.Equal(t,
		[]string{"buildx", "build", "--builder", "devm", "-t", "myimg", "."},
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
		[]string{"--context", "myctx", "buildx", "build", "--builder", "devm", "-t", "myimg", "."},
		got,
	)
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
