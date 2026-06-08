package release

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubLister returns canned data per scope. Used by the pure-function
// Classify tests so no real subprocess fires.
func stubLister(out map[string][]string, errs map[string]error) BrewLister {
	return func(ctx context.Context, scope, name string) ([]string, error) {
		if err, ok := errs[scope]; ok && err != nil {
			return nil, err
		}
		return out[scope], nil
	}
}

func TestClassify_NilListerReturnsManual(t *testing.T) {
	assert.Equal(t, SourceManual, Classify(context.Background(), "/anything", nil))
}

func TestClassify_NotInstalledEitherScopeReturnsManual(t *testing.T) {
	lister := stubLister(nil, map[string]error{
		"--cask":    errors.New("not a cask"),
		"--formula": errors.New("not a formula"),
	})
	got := Classify(context.Background(), "/Users/dev/.local/bin/devm", lister)
	assert.Equal(t, SourceManual, got)
}

func TestClassify_InstalledAsCaskAtMatchingPath(t *testing.T) {
	exec := "/opt/homebrew/Caskroom/devm/0.1.0/devm"
	lister := stubLister(
		map[string][]string{
			"--cask": {
				"/opt/homebrew/Caskroom/devm/0.1.0/LICENSE",
				exec,
				"/opt/homebrew/Caskroom/devm/0.1.0/README.md",
			},
		},
		nil,
	)
	assert.Equal(t, SourceBrew, Classify(context.Background(), exec, lister))
}

func TestClassify_InstalledAsFormulaAtMatchingPath(t *testing.T) {
	exec := "/opt/homebrew/Cellar/devm/0.1.0/bin/devm"
	lister := stubLister(
		map[string][]string{
			"--formula": {
				"/opt/homebrew/Cellar/devm/0.1.0/INSTALL_RECEIPT.json",
				exec,
				"/opt/homebrew/Cellar/devm/0.1.0/.brew/devm.rb",
			},
		},
		map[string]error{"--cask": errors.New("not a cask")},
	)
	assert.Equal(t, SourceBrew, Classify(context.Background(), exec, lister))
}

func TestClassify_CopiedToManualPathReturnsManual(t *testing.T) {
	// User installed via brew, then `cp` to /tmp. The /tmp copy is no
	// longer brew-managed — brew doesn't know about it and won't
	// update it via `brew upgrade`. Treat as self-updateable.
	lister := stubLister(
		map[string][]string{
			"--cask": {"/opt/homebrew/Caskroom/devm/0.1.0/devm"},
		},
		nil,
	)
	got := Classify(context.Background(), "/tmp/devm", lister)
	assert.Equal(t, SourceManual, got)
}

func TestClassify_CustomBrewPrefixAtMatchingPath(t *testing.T) {
	// User has brew installed at ~/brew/ instead of /opt/homebrew.
	// Classify must work because we ASK brew rather than guessing
	// from path prefixes.
	exec := "/Users/x/brew/Caskroom/devm/0.1.0/devm"
	lister := stubLister(
		map[string][]string{"--cask": {exec}},
		nil,
	)
	assert.Equal(t, SourceBrew, Classify(context.Background(), exec, lister))
}

func TestClassify_TimeoutFromListerReturnsManual(t *testing.T) {
	lister := stubLister(nil, map[string]error{
		"--cask":    context.DeadlineExceeded,
		"--formula": context.DeadlineExceeded,
	})
	got := Classify(context.Background(), "/Users/dev/.local/bin/devm", lister)
	assert.Equal(t, SourceManual, got)
}

func TestSourceString(t *testing.T) {
	assert.Equal(t, "brew", SourceBrew.String())
	assert.Equal(t, "manual", SourceManual.String())
}

// ---------- DefaultBrewLister integration via PATH manipulation ----------

func TestDefaultBrewLister_NoBrewOnPath(t *testing.T) {
	t.Setenv("PATH", "/nonexistent")
	lister := DefaultBrewLister()
	assert.Nil(t, lister, "DefaultBrewLister must return nil when brew is not on PATH")
}

func TestDefaultBrewLister_FakeBrewExitsZero_ParsesPaths(t *testing.T) {
	tmp := t.TempDir()
	fakeBrew := filepath.Join(tmp, "brew")
	require.NoError(t, os.WriteFile(fakeBrew, []byte(
		"#!/bin/sh\nprintf '/opt/homebrew/Caskroom/devm/0.1.0/devm\\n/opt/homebrew/Caskroom/devm/0.1.0/LICENSE\\n'\n",
	), 0o755))
	t.Setenv("PATH", tmp)

	lister := DefaultBrewLister()
	require.NotNil(t, lister, "LookPath should find the fake brew")
	paths, err := lister(context.Background(), "--cask", "devm")
	require.NoError(t, err)
	assert.Equal(t, []string{
		"/opt/homebrew/Caskroom/devm/0.1.0/devm",
		"/opt/homebrew/Caskroom/devm/0.1.0/LICENSE",
	}, paths)
}

func TestDefaultBrewLister_FakeBrewExitsOne_ReturnsError(t *testing.T) {
	tmp := t.TempDir()
	fakeBrew := filepath.Join(tmp, "brew")
	// Mirrors the empirical not-installed shape: stderr message, exit 1.
	require.NoError(t, os.WriteFile(fakeBrew, []byte(
		"#!/bin/sh\necho \"Error: No such keg\" >&2\nexit 1\n",
	), 0o755))
	t.Setenv("PATH", tmp)

	lister := DefaultBrewLister()
	require.NotNil(t, lister)
	paths, err := lister(context.Background(), "--cask", "devm")
	require.Error(t, err)
	assert.Nil(t, paths)
}

func TestDefaultBrewLister_BrewHangs_TimesOut(t *testing.T) {
	tmp := t.TempDir()
	fakeBrew := filepath.Join(tmp, "brew")
	require.NoError(t, os.WriteFile(fakeBrew, []byte(
		"#!/bin/sh\nsleep 30\n",
	), 0o755))
	t.Setenv("PATH", tmp)

	lister := DefaultBrewLister()
	require.NotNil(t, lister)
	start := time.Now()
	paths, err := lister(context.Background(), "--cask", "devm")
	elapsed := time.Since(start)
	require.Error(t, err)
	assert.Nil(t, paths)
	assert.Less(t, elapsed, 2*time.Second, "must time out near 1s, not run for 30s")
}
