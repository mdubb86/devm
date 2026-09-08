package serviceapi

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/mdubb86/devm/internal/approve"
	"github.com/mdubb86/devm/internal/identity"
)

// HashCurrentFilesForWatchdog reads devm.yaml + devm.me.yaml from
// macCwd and returns their canonical hashes, for the watchdog's
// approve-state check. devm.me.yaml is optional — its absence hashes
// as approve.HashFile(nil), same as every other approve-gate read
// path.
func HashCurrentFilesForWatchdog(macCwd string) (currentDevmSHA, currentMeSHA string, err error) {
	currentDevm, err := os.ReadFile(filepath.Join(macCwd, "devm.yaml"))
	if err != nil {
		return "", "", err
	}
	var currentMe []byte
	if b, err := os.ReadFile(filepath.Join(macCwd, "devm.me.yaml")); err == nil {
		currentMe = b
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	return approve.HashFile(currentDevm), approve.HashFile(currentMe), nil
}

// ReadApprovedSnapshotForWatchdog wraps approve.Store.Read for the
// watchdog's approve-state check.
func ReadApprovedSnapshotForWatchdog(cfg identity.Config, projectID string) (devmSHA, meSHA string, since *time.Time, hasSnap bool, err error) {
	snap, hasSnap, err := approve.NewStore(cfg).Read(projectID)
	if err != nil {
		return "", "", nil, false, err
	}
	if !hasSnap {
		return "", "", nil, false, nil
	}
	ts := snap.Manifest.Timestamp
	return approve.HashFile(snap.DevmYAML), approve.HashFile(snap.MeYAML), &ts, true, nil
}
