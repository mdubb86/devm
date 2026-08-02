package serviceapi

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildVolumeMountScript_MountsShare(t *testing.T) {
	script := buildVolumeMountScript("pg-data", "/var/lib/postgresql/data", true)
	// virtiofs share mounted at /mnt/vol_pg-data
	assert.Contains(t, script, "mkdir -p /mnt/vol_pg-data")
	assert.Contains(t, script, "mount -t virtiofs vol_pg-data /mnt/vol_pg-data")
}

func TestBuildVolumeMountScript_EmptyMac_EmptyTarget_BindsCleanly(t *testing.T) {
	// Case: Mac empty at boot, target empty in guest → mount + bind, no cp.
	script := buildVolumeMountScript("data", "/data", true)
	// Bind mount emitted:
	assert.Contains(t, script, "mount --bind /mnt/vol_data /data")
	// The script must SKIP cp -a in the empty-target branch:
	// (guest-side `[ -z "$(ls -A /data)" ]` → skip cp, just bind)
	assert.Contains(t, script, `if [ -n "$(ls -A /data 2>/dev/null)" ]`)
}

func TestBuildVolumeMountScript_EmptyMac_TargetHasContent_Adopts(t *testing.T) {
	// Case: Mac empty at boot, target has content → adopt: cp -a target → volume, then bind.
	script := buildVolumeMountScript("pg-data", "/var/lib/postgresql/data", true)
	// cp -a runs FROM target INTO the volume share (before bind):
	assert.Contains(t, script, "cp -a /var/lib/postgresql/data/. /mnt/vol_pg-data/")
	// Then bind:
	assert.Contains(t, script, "mount --bind /mnt/vol_pg-data /var/lib/postgresql/data")
}

func TestBuildVolumeMountScript_NonEmptyMac_TargetEmpty_JustBinds(t *testing.T) {
	// Case: Mac has content (wasEmpty=false), target empty → skip cp, bind.
	script := buildVolumeMountScript("data", "/data", false)
	// wasEmpty=false path must never emit cp -a:
	assert.NotContains(t, script, "cp -a")
	// But still binds:
	assert.Contains(t, script, "mount --bind /mnt/vol_data /data")
}

func TestBuildVolumeMountScript_BothNonEmpty_ErrorsWithConflictMessage(t *testing.T) {
	// Case: Mac has content, target has content → error, do NOT bind.
	// The runtime check happens guest-side; the script emits the
	// conflict-detection + exit-with-clear-message logic gated by
	// wasEmpty=false.
	script := buildVolumeMountScript("pg-data", "/var/lib/postgresql/data", false)
	// The error message names both remediation paths (spec § both-content).
	assert.Contains(t, script, "mount conflict")
	assert.Contains(t, script, "clear guest content")
	assert.Contains(t, script, "clear Mac volume")
	assert.Contains(t, script, "/var/lib/postgresql/data")
	assert.Contains(t, script, "pg-data")
}

func TestBuildVolumeMountScript_AdoptFailure_CleansUpVolumeDir(t *testing.T) {
	// Clean-or-nothing (spec § adopt failure is atomic): if cp -a
	// fails, wipe /mnt/vol_<name>/* so the Mac dir returns to empty,
	// then exit non-zero. Any half-copied files must not survive.
	script := buildVolumeMountScript("pg-data", "/var/lib/postgresql/data", true)
	// The script has an explicit trap or explicit failure branch that
	// wipes /mnt/vol_pg-data/ contents on cp failure:
	assert.Contains(t, script, "rm -rf /mnt/vol_pg-data/")
	// And exits non-zero on that path:
	assert.True(t, strings.Contains(script, "exit 1") || strings.Contains(script, "exit 2"),
		"failure branch must exit non-zero")
}

func TestBuildVolumeMountScript_Idempotent(t *testing.T) {
	// Called on every boot; if the bind mount is already present,
	// running the script again is a no-op that returns success.
	script := buildVolumeMountScript("data", "/data", false)
	// Idempotency guard: mountpoint check on the target path.
	assert.Contains(t, script, "mountpoint -q /data")
}
