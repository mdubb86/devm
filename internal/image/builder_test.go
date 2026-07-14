package image

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	baseimage "github.com/mdubb86/devm/image"
	"github.com/mdubb86/devm/internal/schema"
)

// DefinitionHash is a pure function of embedded content — the
// provisioning script, the cleanup fragment, and definitionVersion.

func TestDefinitionHash_StableAcrossCalls(t *testing.T) {
	h1, err := DefinitionHash()
	require.NoError(t, err)
	h2, err := DefinitionHash()
	require.NoError(t, err)
	assert.Equal(t, h1, h2, "same embedded inputs must hash the same")
	assert.Len(t, h1, 64, "sha256 hex must be 64 chars")
}

// TestDefinitionHash_MatchesFormula recomputes sha256(script + 0x00 +
// cleanup + 0x00 + version + 0x00 + disk size) directly against this
// package's unexported inputs, so the test fails loudly if the hash
// formula (or its inputs) ever changes silently without a
// definitionVersion bump.
func TestDefinitionHash_MatchesFormula(t *testing.T) {
	h := sha256.New()
	io.WriteString(h, baseimage.ProvisionBaseScript)
	h.Write([]byte{0})
	io.WriteString(h, cleanupScript)
	h.Write([]byte{0})
	io.WriteString(h, definitionVersion)
	h.Write([]byte{0})
	io.WriteString(h, strconv.Itoa(schema.DefaultDiskSizeGB))
	want := hex.EncodeToString(h.Sum(nil))

	got, err := DefinitionHash()
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestHashStorePath_HomeRelative(t *testing.T) {
	p, err := HashStorePath()
	require.NoError(t, err)
	assert.Contains(t, p, "Library/Caches/devm/base-image.hash")
}

// NeedsBuild's "VM absent" branch depends on Tart being installed
// (baseImageExists shells out to `tart list`). We test the
// hash-mismatch branch here, which doesn't require Tart: with no
// stored hash on disk, NeedsBuild must report true unconditionally.
func TestNeedsBuild_TrueWhenNoStoredHash(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	needs, hash, err := NeedsBuild()
	require.NoError(t, err)
	assert.True(t, needs, "no stored hash means a build is needed")
	assert.Len(t, hash, 64)
}

// TestNeedsBuild_HashMatchesStored verifies NeedsBuild returns the
// current definition hash regardless of its boolean verdict. The
// verdict itself depends on baseImageExists, which shells out to the
// real `tart list` — in CI/dev environments without Tart installed
// (or without devm-base built), that reports false, so NeedsBuild
// would report true even with a matching stored hash. That's the
// documented "VM absent" branch, not a bug, so we don't assert the
// boolean here — doing so would make the test's outcome depend on the
// local machine's Tart state.
func TestNeedsBuild_HashMatchesStored(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cur, err := DefinitionHash()
	require.NoError(t, err)

	storePath, err := HashStorePath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(storePath), 0700))
	require.NoError(t, os.WriteFile(storePath, []byte(cur), 0644))

	_, hash, err := NeedsBuild()
	require.NoError(t, err)
	assert.Equal(t, cur, hash)
}
