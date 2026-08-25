package serviceapi

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/mdubb86/devm/internal/daemonlog"
	"github.com/mdubb86/devm/internal/identity"
)

// projectIPPoolFmt is the lo0 alias address format. Hardcoded — it's
// a devm-wide convention, not per-profile identity like the pool
// bounds (cfg.PoolStart / cfg.PoolEnd).
const projectIPPoolFmt = "127.42.0.%d"

// allocMu serializes the read-state/decide/write critical section of
// AllocateProjectIP and ReleaseProjectIP against each other. Without
// it, concurrent /vm/start calls for different projects could both
// read ironProxyState before either had written its choice, compute
// the same lowest-free IP, and both write it — a TOCTOU race across
// the three separate lock acquisitions (get, keys+get loop, put) on
// the underlying projectInfoStore.
var allocMu sync.Mutex

// probeIPInUse reports whether a listener is already accepting on
// ip:22. Every project's softnet binds :22 on its ProjectIP, so an
// accepting listener on an address the daemon considers free means a
// process outside daemon state holds it — typically an orphaned VM
// whose state snapshot was lost (daemon state and running VMs have
// independent lifetimes: VMs survive daemon uninstall/reinstall and
// state-dir loss). Handing out such an address cross-wires DNS and
// ingress: the name resolves to the new project while :22 keeps
// answering with the orphan's sshd, and the bind conflict is only
// visible as a swallowed EADDRINUSE inside the new softnet.
// Var so unit tests can stub machine state.
var probeIPInUse = func(ip string) bool {
	return listenerActive(net.JoinHostPort(ip, "22"))
}

// listenerActive reports whether a TCP connect to addr succeeds.
// Loopback connects resolve instantly (accept or ECONNREFUSED); the
// timeout only guards pathological states.
func listenerActive(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 250*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// AllocateProjectIP returns projectID's existing ProjectIP if it has
// one; otherwise picks the lowest-free address from cfg's alias pool,
// records it in ironProxyState and StateSnapshot, and returns it.
// Fails when the pool is exhausted.
func AllocateProjectIP(cfg identity.Config, projectID string) (string, error) {
	allocMu.Lock()
	defer allocMu.Unlock()
	if existing, ok := ironProxyState.get(projectID); ok && existing.ProjectIP != "" {
		return existing.ProjectIP, nil
	}
	// Collect in-use IPs from all currently-tracked projects.
	inUse := make(map[string]bool, cfg.PoolEnd)
	for _, id := range ironProxyState.keys() {
		info, ok := ironProxyState.get(id)
		if ok && info.ProjectIP != "" {
			inUse[info.ProjectIP] = true
		}
	}
	for n := cfg.PoolStart; n <= cfg.PoolEnd; n++ {
		ip := fmt.Sprintf(projectIPPoolFmt, n)
		if inUse[ip] {
			continue
		}
		// The project's own softnet binds nothing until the expose map
		// is pushed (after allocation), so any :22 listener here is
		// foreign — skip the address rather than cross-wire it.
		if probeIPInUse(ip) {
			daemonlog.Errorf("serviceapi: pool IP %s:22 is held by a listener outside daemon state (orphaned VM?) — skipping it for %s", ip, projectID)
			continue
		}
		// Store on projectInfo (and merge into any pre-existing entry).
		info, _ := ironProxyState.get(projectID)
		info.ProjectIP = ip
		ironProxyState.put(projectID, info)
		// Mirror to StateSnapshot.
		if snap, err := ReadStateSnapshot(cfg, projectID); err == nil && snap != nil {
			snap.ProjectIP = ip
			_ = WriteStateSnapshot(cfg, projectID, *snap)
		}
		return ip, nil
	}
	return "", fmt.Errorf("project IP pool exhausted (%d concurrent projects): free a slot with `devm stop`", cfg.PoolEnd-cfg.PoolStart+1)
}

// ReleaseProjectIP clears projectID's ProjectIP from both projectInfo
// and StateSnapshot. Idempotent — call at /vm/stop.
func ReleaseProjectIP(cfg identity.Config, projectID string) {
	allocMu.Lock()
	defer allocMu.Unlock()
	info, ok := ironProxyState.get(projectID)
	if ok && info.ProjectIP != "" {
		info.ProjectIP = ""
		ironProxyState.put(projectID, info)
	}
	if snap, err := ReadStateSnapshot(cfg, projectID); err == nil && snap != nil {
		if snap.ProjectIP != "" {
			snap.ProjectIP = ""
			_ = WriteStateSnapshot(cfg, projectID, *snap)
		}
	}
}
