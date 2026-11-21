package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mdubb86/devm/internal/secret"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSecretSet_StoresInBackend(t *testing.T) {
	b := secret.NewFake()
	err := runSecretSet(b, "proj", "github_token", strings.NewReader("ghp_abc\n"))
	require.NoError(t, err)

	got, err := b.Get("proj/github_token")
	require.NoError(t, err)
	assert.Equal(t, "ghp_abc", got)
}

func TestSecretGet_Masked(t *testing.T) {
	b := secret.NewFake()
	require.NoError(t, b.Set("proj/x", "supersecretvalue"))

	var out bytes.Buffer
	require.NoError(t, runSecretGet(b, "proj", "x", false, &out))
	got := strings.TrimSpace(out.String())
	assert.NotContains(t, got, "supersecretvalue")
	assert.True(t, strings.HasPrefix(got, "su"), "masked output should keep a leading hint: got %q", got)
}

func TestSecretGet_Reveal(t *testing.T) {
	b := secret.NewFake()
	require.NoError(t, b.Set("proj/x", "rawvalue"))

	var out bytes.Buffer
	require.NoError(t, runSecretGet(b, "proj", "x", true, &out))
	assert.Equal(t, "rawvalue", strings.TrimSpace(out.String()))
}

func TestSecretList_OnlyShowsCurrentProject(t *testing.T) {
	b := secret.NewFake()
	require.NoError(t, b.Set("alpha/x", "1"))
	require.NoError(t, b.Set("alpha/y", "2"))
	require.NoError(t, b.Set("beta/z", "3"))

	var out bytes.Buffer
	require.NoError(t, runSecretList(b, "alpha", &out))
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	assert.ElementsMatch(t, []string{"x", "y"}, lines)
}

func TestSecretDelete_Removes(t *testing.T) {
	b := secret.NewFake()
	require.NoError(t, b.Set("proj/x", "1"))

	require.NoError(t, runSecretDelete(b, "proj", "x"))
	_, err := b.Get("proj/x")
	assert.ErrorIs(t, err, secret.ErrNotFound)
}
