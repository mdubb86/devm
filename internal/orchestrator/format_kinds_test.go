package orchestrator

import (
	"testing"

	"github.com/mdubb86/devm/internal/reconcile"
	"github.com/stretchr/testify/assert"
)

// Every ChangeKind must have a JSON name and a human rendering; a kind
// added to the reconcile package without touching format.go fails here.
func TestEveryChangeKindIsFormatted(t *testing.T) {
	for _, k := range reconcile.AllKinds() {
		assert.NotEqual(t, "unknown", changeKindJSON(k), "changeKindJSON missing kind %d", k)
		c := reconcile.Change{Kind: k}
		if k.Bucket() == reconcile.BucketEgressRestart {
			assert.NotEqual(t, "(unknown iron-proxy change)", formatIronProxyChange(c), "formatIronProxyChange missing kind %d", k)
			continue
		}
		assert.NotEqual(t, "(unknown change)", formatChange(c), "formatChange missing kind %d", k)
	}
}

func TestFormatChange_Cron(t *testing.T) {
	added := reconcile.Change{Kind: reconcile.KindCronAdd, Key: "backup", New: "0 3 * * *"}
	assert.Equal(t, `+ cron backup: "0 3 * * *"`, formatChange(added))
	assert.Equal(t, "cron_add", changeKindJSON(reconcile.KindCronAdd))

	removed := reconcile.Change{Kind: reconcile.KindCronRemove, Key: "backup", Old: "0 3 * * *"}
	assert.Equal(t, "- cron backup", formatChange(removed))
	assert.Equal(t, "cron_remove", changeKindJSON(reconcile.KindCronRemove))

	changed := reconcile.Change{Kind: reconcile.KindCronChange, Key: "backup", Old: "0 3 * * *", New: "0 4 * * *"}
	assert.Equal(t, `~ cron backup: "0 3 * * *" → "0 4 * * *"`, formatChange(changed))
	assert.Equal(t, "cron_change", changeKindJSON(reconcile.KindCronChange))
}

func TestFormatChange_SnapshotEntry(t *testing.T) {
	assert.Equal(t, "+ snapshot pg", formatChange(reconcile.Change{Kind: reconcile.KindSnapshotEntryAdd, Key: "pg"}))
	assert.Equal(t, "- snapshot pg", formatChange(reconcile.Change{Kind: reconcile.KindSnapshotEntryRemove, Key: "pg"}))
	assert.Equal(t, "~ snapshot pg", formatChange(reconcile.Change{Kind: reconcile.KindSnapshotEntryChange, Key: "pg"}))
	assert.Equal(t, "snapshot_entry_add", changeKindJSON(reconcile.KindSnapshotEntryAdd))
	assert.Equal(t, "snapshot_entry_remove", changeKindJSON(reconcile.KindSnapshotEntryRemove))
	assert.Equal(t, "snapshot_entry_change", changeKindJSON(reconcile.KindSnapshotEntryChange))
}
