package serviceapi

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testPopSessionCfg returns an identity.Config pointed at a scratch HOME
// so RuntimeDir()-derived paths never touch the real ~/Library.
func testPopSessionCfg(t *testing.T) identity.Config {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	return identity.Config{Name: "devm-test-pop-session"}
}

func TestPopSessionID_DeterministicHexShape(t *testing.T) {
	id1 := popSessionID("/tmp/site/index.html")
	id2 := popSessionID("/tmp/site/index.html")
	id3 := popSessionID("/tmp/site/other.html")

	assert.Equal(t, id1, id2, "same input → same id")
	assert.NotEqual(t, id1, id3, "different input → different id")
	assert.Len(t, id1, 12)
	assert.Regexp(t, "^[0-9a-f]{12}$", id1)
}

func TestPopSessionMutagenName_Format(t *testing.T) {
	got := popSessionMutagenName("myproj", "abc123def456")
	assert.Equal(t, "pop-myproj-abc123def456", got)
}

func TestPopSessionStore_GetOrCreate_NewSession(t *testing.T) {
	store := NewPopSessionStore()
	cfg := testPopSessionCfg(t)

	var createCalls int
	session, created, err := store.GetOrCreate(cfg, "myproj", "/tmp/index.html", PopKindFile, func(ps *PopSession) error {
		createCalls++
		ps.MutagenSessionID = "mut-sess-123"
		return nil
	})
	require.NoError(t, err)
	require.NotNil(t, session)
	assert.True(t, created)
	assert.Equal(t, 1, createCalls)
	assert.Equal(t, "myproj", session.ProjectName)
	assert.Equal(t, "/tmp/index.html", session.GuestPath)
	assert.Equal(t, "index.html", session.TargetName)
	assert.Equal(t, PopKindFile, session.Kind)
	assert.Equal(t, "mut-sess-123", session.MutagenSessionID)
	assert.Equal(t, filepath.Join(PopScratchRoot(cfg), session.ID), session.MacDir)
	assert.False(t, session.CreatedAt.IsZero())
}

func TestPopSessionStore_GetOrCreate_DedupeSamePath(t *testing.T) {
	store := NewPopSessionStore()
	cfg := testPopSessionCfg(t)

	var creates int
	for i := 0; i < 3; i++ {
		_, created, err := store.GetOrCreate(cfg, "myproj", "/tmp/a.html", PopKindFile, func(ps *PopSession) error {
			creates++
			ps.MutagenSessionID = "id"
			return nil
		})
		require.NoError(t, err)
		if i == 0 {
			assert.True(t, created)
		} else {
			assert.False(t, created, "second+ pop of same path must not create")
		}
	}
	assert.Equal(t, 1, creates, "create func called once")
}

func TestPopSessionStore_GetOrCreate_DirKind_NoTargetName(t *testing.T) {
	store := NewPopSessionStore()
	cfg := testPopSessionCfg(t)
	session, _, err := store.GetOrCreate(cfg, "myproj", "/tmp/site", PopKindDir, func(ps *PopSession) error {
		ps.MutagenSessionID = "id"
		return nil
	})
	require.NoError(t, err)
	assert.Empty(t, session.TargetName, "dir-scope session has empty TargetName")
}

func TestPopSessionStore_GetOrCreate_ConcurrentSamePath_CollapseToOne(t *testing.T) {
	store := NewPopSessionStore()
	cfg := testPopSessionCfg(t)

	var createCount int
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := store.GetOrCreate(cfg, "p", "/tmp/x", PopKindFile, func(ps *PopSession) error {
				createCount++
				ps.MutagenSessionID = "id"
				return nil
			})
			require.NoError(t, err)
		}()
	}
	wg.Wait()
	assert.Equal(t, 1, createCount, "concurrent creates for same path collapse into one")
}

func TestPopSessionStore_GetOrCreate_CreateErrorNotStored(t *testing.T) {
	store := NewPopSessionStore()
	cfg := testPopSessionCfg(t)

	_, _, err := store.GetOrCreate(cfg, "p", "/tmp/x", PopKindFile, func(ps *PopSession) error {
		return assert.AnError
	})
	require.Error(t, err)

	// A subsequent call should attempt to create again — the failed
	// create left no entry behind.
	var secondCalled bool
	_, created, err2 := store.GetOrCreate(cfg, "p", "/tmp/x", PopKindFile, func(ps *PopSession) error {
		secondCalled = true
		ps.MutagenSessionID = "id"
		return nil
	})
	require.NoError(t, err2)
	assert.True(t, created)
	assert.True(t, secondCalled)
}

func TestPopSessionStore_RemoveByID_ReturnsRemoved(t *testing.T) {
	store := NewPopSessionStore()
	cfg := testPopSessionCfg(t)
	session, _, err := store.GetOrCreate(cfg, "p", "/tmp/x", PopKindFile, func(ps *PopSession) error {
		ps.MutagenSessionID = "id"
		return nil
	})
	require.NoError(t, err)

	removed := store.RemoveByID(session.ID)
	require.NotNil(t, removed)
	assert.Equal(t, "/tmp/x", removed.GuestPath)

	// Second remove is a no-op.
	assert.Nil(t, store.RemoveByID(session.ID))
}

func TestPopSessionStore_ListForProject_SortedByCreatedAt(t *testing.T) {
	store := NewPopSessionStore()
	cfg := testPopSessionCfg(t)

	for _, gp := range []string{"/tmp/a", "/tmp/b", "/tmp/c"} {
		_, _, err := store.GetOrCreate(cfg, "p", gp, PopKindFile, func(ps *PopSession) error {
			ps.MutagenSessionID = "id"
			return nil
		})
		require.NoError(t, err)
		time.Sleep(2 * time.Millisecond) // ensure distinct CreatedAt
	}

	list := store.ListForProject("p")
	require.Len(t, list, 3)
	for i := 1; i < len(list); i++ {
		assert.False(t, list[i].CreatedAt.Before(list[i-1].CreatedAt),
			"ListForProject must be sorted ascending by CreatedAt")
	}
}
