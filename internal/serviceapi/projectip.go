package serviceapi

import (
	"fmt"
	"os"
	"sync"
)

const (
	projectIPPoolStart = 1
	projectIPPoolEnd   = 20
	projectIPPoolFmt   = "127.42.0.%d"

	// fallbackProjectIP is what AllocateProjectIP hands out when the
	// portbinder helper isn't available (see fallback.go). Matches
	// pre-B3 behavior: every project's services bind loopback directly,
	// no per-project alias needed.
	fallbackProjectIP = "127.0.0.1"
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
//
// When the portbinder helper isn't available (helperAvailable false —
// see fallback.go), the pool is skipped entirely: every project gets
// the fixed fallbackProjectIP (127.0.0.1), matching pre-B3 behavior.
//
// DEVM_PROJECT_IP_ALLOC_DIRECTION=reverse walks the pool from
// 127.42.0.20 down to .1 instead of the default .1 up to .20. Used by
// the B3 standalone-tests e2e lane so its sandbox daemon — which talks
// to the SAME real portbinder helper a production daemon on the same
// machine might also be using — claims slots from the opposite end of
// the pool, avoiding collisions with the user's live projects short of
// 20 concurrent projects total.
func AllocateProjectIP(projectID string) (string, error) {
	allocMu.Lock()
	defer allocMu.Unlock()
	if existing, ok := ironProxyState.get(projectID); ok && existing.ProjectIP != "" {
		return existing.ProjectIP, nil
	}
	if !helperAvailable {
		info, _ := ironProxyState.get(projectID)
		info.ProjectIP = fallbackProjectIP
		ironProxyState.put(projectID, info)
		if snap, err := ReadStateSnapshot(projectID); err == nil && snap != nil {
			snap.ProjectIP = fallbackProjectIP
			_ = WriteStateSnapshot(projectID, *snap)
		}
		return fallbackProjectIP, nil
	}
	// Collect in-use IPs from all currently-tracked projects.
	inUse := make(map[string]bool, projectIPPoolEnd)
	for _, id := range ironProxyState.keys() {
		info, ok := ironProxyState.get(id)
		if ok && info.ProjectIP != "" {
			inUse[info.ProjectIP] = true
		}
	}
	for _, n := range projectIPPoolOrder() {
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

// projectIPPoolOrder returns the pool's slot numbers in allocation
// order: ascending (1..20) by default, or descending (20..1) when
// DEVM_PROJECT_IP_ALLOC_DIRECTION=reverse.
func projectIPPoolOrder() []int {
	nums := make([]int, 0, projectIPPoolEnd-projectIPPoolStart+1)
	for n := projectIPPoolStart; n <= projectIPPoolEnd; n++ {
		nums = append(nums, n)
	}
	if os.Getenv("DEVM_PROJECT_IP_ALLOC_DIRECTION") == "reverse" {
		for i, j := 0, len(nums)-1; i < j; i, j = i+1, j-1 {
			nums[i], nums[j] = nums[j], nums[i]
		}
	}
	return nums
}

// ReleaseProjectIP clears projectID's ProjectIP and PickedSSHPort from
// both projectInfo and StateSnapshot. Idempotent — call at /vm/stop.
func ReleaseProjectIP(projectID string) {
	allocMu.Lock()
	defer allocMu.Unlock()
	info, ok := ironProxyState.get(projectID)
	if ok && (info.ProjectIP != "" || info.PickedSSHPort != 0) {
		info.ProjectIP = ""
		info.PickedSSHPort = 0
		ironProxyState.put(projectID, info)
	}
	if snap, err := ReadStateSnapshot(projectID); err == nil && snap != nil {
		if snap.ProjectIP != "" || snap.PickedSSHPort != 0 {
			snap.ProjectIP = ""
			snap.PickedSSHPort = 0
			_ = WriteStateSnapshot(projectID, *snap)
		}
	}
}

// AllocateSSHPort returns projectID's fallback-mode SSH host port,
// allocating one via pickPort() the first time and persisting it the
// same way AllocateProjectIP persists ProjectIP. Idempotent.
//
// When the portbinder helper is available, SSH binds directly on the
// project's ProjectIP:22 (no host port to pick), so this always
// returns 0 without touching state.
func AllocateSSHPort(projectID string) (int, error) {
	if helperAvailable {
		return 0, nil
	}
	allocMu.Lock()
	defer allocMu.Unlock()
	if existing, ok := ironProxyState.get(projectID); ok && existing.PickedSSHPort != 0 {
		return existing.PickedSSHPort, nil
	}
	port, err := pickPort()
	if err != nil {
		return 0, fmt.Errorf("pick ssh host port: %w", err)
	}
	info, _ := ironProxyState.get(projectID)
	info.PickedSSHPort = port
	ironProxyState.put(projectID, info)
	if snap, err := ReadStateSnapshot(projectID); err == nil && snap != nil {
		snap.PickedSSHPort = port
		_ = WriteStateSnapshot(projectID, *snap)
	}
	return port, nil
}
