package serviceapi

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGuardCheck_BothEmpty_OK verifies an empty Mac side and an empty
// guest side always pass — there's nothing to conflict over.
func TestGuardCheck_BothEmpty_OK(t *testing.T) {
	verdict := GuardCheck(ScanSide{}, ScanSide{})
	assert.True(t, verdict.OK)
}

// TestGuardCheck_MacPopulatedGuestEmpty_OK verifies a populated Mac side
// against an empty guest side passes — the guest is the target of the
// initial sync, nothing to reconcile.
func TestGuardCheck_MacPopulatedGuestEmpty_OK(t *testing.T) {
	mac := ScanSide{Count: 3, Size: 100, TopHash: "abc"}
	verdict := GuardCheck(mac, ScanSide{})
	assert.True(t, verdict.OK)
}

// TestGuardCheck_GuestPopulatedMacEmpty_OK verifies a populated guest
// side against an empty Mac side passes symmetrically.
func TestGuardCheck_GuestPopulatedMacEmpty_OK(t *testing.T) {
	guest := ScanSide{Count: 3, Size: 100, TopHash: "abc"}
	verdict := GuardCheck(ScanSide{}, guest)
	assert.True(t, verdict.OK)
}

// TestGuardCheck_BothAligned_OK verifies both sides populated with
// matching count/size/TopHash pass.
func TestGuardCheck_BothAligned_OK(t *testing.T) {
	side := ScanSide{Count: 3, Size: 100, TopHash: "abc"}
	verdict := GuardCheck(side, side)
	assert.True(t, verdict.OK)
}

// TestGuardCheck_CountDiffers_Rejects verifies a count mismatch between
// two populated sides is rejected, with both counts named in the
// reason.
func TestGuardCheck_CountDiffers_Rejects(t *testing.T) {
	mac := ScanSide{Count: 3, Size: 100, TopHash: "abc"}
	guest := ScanSide{Count: 5, Size: 100, TopHash: "abc"}

	verdict := GuardCheck(mac, guest)

	require.False(t, verdict.OK)
	assert.Contains(t, verdict.Reason, "3")
	assert.Contains(t, verdict.Reason, "5")
}

// TestGuardCheck_TopHashDiffers_Rejects verifies a TopHash mismatch
// (same count/size, different content shape) is rejected with a reason
// mentioning "content shape".
func TestGuardCheck_TopHashDiffers_Rejects(t *testing.T) {
	mac := ScanSide{Count: 3, Size: 100, TopHash: "abc"}
	guest := ScanSide{Count: 3, Size: 100, TopHash: "def"}

	verdict := GuardCheck(mac, guest)

	require.False(t, verdict.OK)
	assert.Contains(t, verdict.Reason, "content shape")
}

// TestScanMac_TreeWithFiles verifies ScanMac walks a populated tempdir
// and returns a consistent Count/Size/TopHash.
func TestScanMac_TreeWithFiles(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello"), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "sub"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "sub", "b.txt"), []byte("world!"), 0644))

	side, err := ScanMac(root)
	require.NoError(t, err)

	assert.EqualValues(t, 3, side.Count) // a.txt, sub/, sub/b.txt
	assert.EqualValues(t, 11, side.Size) // "hello" + "world!"
	assert.NotEmpty(t, side.TopHash)

	// Re-scanning the same tree must produce an identical hash.
	again, err := ScanMac(root)
	require.NoError(t, err)
	assert.Equal(t, side.TopHash, again.TopHash)
}

// TestScanMac_EmptyDir verifies an empty directory scans to an
// all-zero, empty-hash ScanSide.
func TestScanMac_EmptyDir(t *testing.T) {
	root := t.TempDir()

	side, err := ScanMac(root)
	require.NoError(t, err)

	assert.EqualValues(t, 0, side.Count)
	assert.EqualValues(t, 0, side.Size)
	assert.Empty(t, side.TopHash)
}

// TestScanGuest_ParsesOutput verifies ScanGuest parses a fake exec's
// stdout into the expected ScanSide fields.
func TestScanGuest_ParsesOutput(t *testing.T) {
	fake := func(script string) (string, string, int, error) {
		return "count=5 size=1024 hash=abcdef\n", "", 0, nil
	}

	side, err := ScanGuest(fake, "/data")
	require.NoError(t, err)

	assert.EqualValues(t, 5, side.Count)
	assert.EqualValues(t, 1024, side.Size)
	assert.Equal(t, "abcdef", side.TopHash)
}

// TestScanGuest_MissingTarget_ReturnsEmpty verifies ScanGuest treats the
// script's "count=0 size=0 hash=-" sentinel (missing or empty target) as
// an empty ScanSide with no TopHash.
func TestScanGuest_MissingTarget_ReturnsEmpty(t *testing.T) {
	fake := func(script string) (string, string, int, error) {
		return "count=0 size=0 hash=-\n", "", 0, nil
	}

	side, err := ScanGuest(fake, "/does/not/exist")
	require.NoError(t, err)

	assert.EqualValues(t, 0, side.Count)
	assert.EqualValues(t, 0, side.Size)
	assert.Empty(t, side.TopHash)
}

// TestBuildGuestScanScript_QuotesInjectionAttempt verifies a guestPath
// containing a shell command substitution is neutralized by
// buildGuestScanScript's POSIX single-quoting rather than executed:
// running the generated script through sh must not create the marker
// file the payload targets, and the payload must survive intact inside
// single-quotes in the generated script text.
func TestBuildGuestScanScript_QuotesInjectionAttempt(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "should_not_exist")
	guestPath := "/data/$(touch " + marker + ")"

	script := buildGuestScanScript(guestPath)
	assert.Contains(t, script, "$(touch "+marker+")",
		"payload must appear literally (unexecuted) in the generated script")

	out, err := exec.Command("sh", "-c", script).CombinedOutput()
	require.NoError(t, err, "script output: %s", out)

	_, statErr := os.Stat(marker)
	assert.True(t, os.IsNotExist(statErr), "command substitution in guestPath must not execute")
}
