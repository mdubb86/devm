package secret

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFakeBackend_SetGetDelete(t *testing.T) {
	b := NewFake()

	require.NoError(t, b.Set("proj/foo", "secret-value"))
	got, err := b.Get("proj/foo")
	require.NoError(t, err)
	assert.Equal(t, "secret-value", got)

	require.NoError(t, b.Delete("proj/foo"))
	_, err = b.Get("proj/foo")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestFakeBackend_List_FiltersByProjectPrefix(t *testing.T) {
	b := NewFake()
	require.NoError(t, b.Set("alpha/x", "1"))
	require.NoError(t, b.Set("alpha/y", "2"))
	require.NoError(t, b.Set("beta/x", "3"))

	names, err := b.List("alpha")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"x", "y"}, names)
}

func TestFakeBackend_Get_NotFound(t *testing.T) {
	b := NewFake()
	_, err := b.Get("missing/key")
	assert.ErrorIs(t, err, ErrNotFound)
}
