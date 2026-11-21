package snapshot

import (
	"path/filepath"
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/stretchr/testify/assert"
)

func TestSource_Constants(t *testing.T) {
	assert.Equal(t, Source("cli"), SourceCLI)
	assert.Equal(t, Source("on_stop"), SourceOnStop)
	assert.Equal(t, Source("on_teardown"), SourceOnTeardown)
	assert.Equal(t, Source("cron"), SourceCron)
	assert.Equal(t, Source("unknown"), SourceUnknown)
}

func TestEntryDir_SiblingToVolumesMirror(t *testing.T) {
	ident := identity.Config{Name: "devm-test"}
	got := EntryDir(ident, "proj-xyz", "pg-data")
	want := filepath.Join(ident.RuntimeDir(), "proj-xyz", "snapshots", "pg-data")
	assert.Equal(t, want, got)
}
