package serviceapi

import (
	"fmt"
	"sync"
)

const (
	projectIPPoolStart = 1
	projectIPPoolEnd   = 20
	projectIPPoolFmt   = "127.42.0.%d"
)

// allocMu serializes the read-state/decide/write critical section of
// AllocateProjectIP and ReleaseProjectIP against each other. Without
// it, concurrent /vm/start calls for different projects could both
// read ironProxyState before either had written its choice, compute
// the same lowest-free IP, and both write it — a TOCTOU race across
// the three separate lock acquisitions (get, keys+get loop, put) on
// the underlying projectInfoStore.
var allocMu sync.Mutex

// AllocateProjectIP returns projectID's existing ProjectIP if it has
// one; otherwise picks the lowest-free address from 127.42.0.1..20,
// records it in ironProxyState and StateSnapshot, and returns it.
// Fails when the pool is exhausted (20 concurrent projects).
func AllocateProjectIP(projectID string) (string, error) {
	allocMu.Lock()
	defer allocMu.Unlock()
	if existing, ok := ironProxyState.get(projectID); ok && existing.ProjectIP != "" {
		return existing.ProjectIP, nil
	}
	// Collect in-use IPs from all currently-tracked projects.
	inUse := make(map[string]bool, projectIPPoolEnd)
	for _, id := range ironProxyState.keys() {
		info, ok := ironProxyState.get(id)
		if ok && info.ProjectIP != "" {
			inUse[info.ProjectIP] = true
		}
	}
	for n := projectIPPoolStart; n <= projectIPPoolEnd; n++ {
		ip := fmt.Sprintf(projectIPPoolFmt, n)
		if inUse[ip] {
			continue
		}
		// Store on projectInfo (and merge into any pre-existing entry).
		info, _ := ironProxyState.get(projectID)
		info.ProjectIP = ip
		ironProxyState.put(projectID, info)
		// Mirror to StateSnapshot.
		if snap, err := ReadStateSnapshot(projectID); err == nil && snap != nil {
			snap.ProjectIP = ip
			_ = WriteStateSnapshot(projectID, *snap)
		}
		return ip, nil
	}
	return "", fmt.Errorf("project IP pool exhausted (20 concurrent projects): free a slot with `devm stop`")
}

// ReleaseProjectIP clears projectID's ProjectIP from both projectInfo
// and StateSnapshot. Idempotent — call at /vm/stop.
func ReleaseProjectIP(projectID string) {
	allocMu.Lock()
	defer allocMu.Unlock()
	info, ok := ironProxyState.get(projectID)
	if ok && info.ProjectIP != "" {
		info.ProjectIP = ""
		ironProxyState.put(projectID, info)
	}
	if snap, err := ReadStateSnapshot(projectID); err == nil && snap != nil {
		if snap.ProjectIP != "" {
			snap.ProjectIP = ""
			_ = WriteStateSnapshot(projectID, *snap)
		}
	}
}
