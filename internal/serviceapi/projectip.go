package serviceapi

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

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

// probeIPInUse reports whether a listener is bound SPECIFICALLY to
// ip:22. Every project's softnet binds :22 on its ProjectIP with a
// specific-address bind, so a specific-address LISTEN on an IP the
// daemon considers free means a process outside daemon state holds
// it — typically an orphaned VM whose state snapshot was lost
// (daemon state and running VMs have independent lifetimes: VMs
// survive daemon uninstall/reinstall and state-dir loss). Handing
// out such an address cross-wires DNS and ingress: the name resolves
// to the new project while :22 keeps answering with the orphan's
// sshd, and the bind conflict is only visible as a swallowed
// EADDRINUSE inside the new softnet.
//
// A wildcard listener on *:22 (host sshd with Remote Login enabled)
// accepts on every lo0 alias but is NOT a specific bind, so it does
// not count — a connect-based probe would false-positive the whole
// pool in that case, since the wildcard socket answers on every
// alias.
//
// Var so unit tests can stub machine state. On query error, treat
// the slot as occupied (fail closed) and log the failure.
var probeIPInUse = func(ip string) bool {
	ports, err := listenersBoundTo(ip)
	if err != nil {
		daemonlog.Errorf("serviceapi: listener query for %s failed: %v — treating slot as occupied", ip, err)
		return true
	}
	for _, p := range ports {
		if p == 22 {
			return true
		}
	}
	return false
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
	// Collect in-use IPs with the project they belong to, so an
	// exhaustion error can name which project holds each slot.
	heldBy := make(map[string]string, cfg.PoolEnd)
	for _, id := range ironProxyState.keys() {
		info, ok := ironProxyState.get(id)
		if ok && info.ProjectIP != "" {
			heldBy[info.ProjectIP] = id
		}
	}
	var listenerHeld []string
	for n := cfg.PoolStart; n <= cfg.PoolEnd; n++ {
		ip := fmt.Sprintf(projectIPPoolFmt, n)
		if _, held := heldBy[ip]; held {
			continue
		}
		// The project's own softnet binds nothing until the expose map
		// is pushed (after allocation), so any specific-address :22
		// listener here is foreign — skip the address rather than
		// cross-wire it. (A wildcard *:22 is not a specific bind and
		// does not trigger this; see probeIPInUse.)
		if probeIPInUse(ip) {
			daemonlog.Errorf("serviceapi: pool IP %s has a specific LISTEN on :22 outside daemon state (orphan softnet, or sshd ListenAddress %s) — skipping it for %s", ip, ip, projectID)
			listenerHeld = append(listenerHeld, ip)
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
	return "", poolExhaustedError(cfg, heldBy, listenerHeld)
}

// poolExhaustedError formats the exhaustion error with each slot's
// reason — which project claims it, or that a non-devm specific
// listener on :22 blocked it — so the operator can identify what to
// free without hunting for phantom projects. heldBy is ip→projectID
// from ironProxyState; listenerHeld is the slots blocked by the
// specific-listener probe.
func poolExhaustedError(cfg identity.Config, heldBy map[string]string, listenerHeld []string) error {
	size := cfg.PoolEnd - cfg.PoolStart + 1
	byProject := make([]string, 0, len(heldBy))
	for ip, id := range heldBy {
		byProject = append(byProject, fmt.Sprintf("%s=%s", ip, id))
	}
	sort.Slice(byProject, func(i, j int) bool { return lastOctet(byProject[i]) < lastOctet(byProject[j]) })
	sort.Slice(listenerHeld, func(i, j int) bool { return lastOctet(listenerHeld[i]) < lastOctet(listenerHeld[j]) })
	var b strings.Builder
	fmt.Fprintf(&b, "project IP pool exhausted (%d slots)", size)
	if len(byProject) > 0 {
		fmt.Fprintf(&b, "; held by projects: %s", strings.Join(byProject, ", "))
	}
	if len(listenerHeld) > 0 {
		fmt.Fprintf(&b, "; blocked by non-devm specific LISTEN on :22: %s (orphan softnet, or an sshd ListenAddress bound to these IPs — a wildcard *:22 from macOS Remote Login does NOT trigger this)", strings.Join(listenerHeld, ", "))
	}
	b.WriteString("; free a slot with `devm stop <project>` or remove the blocking listener")
	return fmt.Errorf("%s", b.String())
}

// lastOctet parses the trailing N from "127.42.0.N" or "127.42.0.N=…"
// so pool IPs sort numerically rather than lexicographically. Returns
// a large sentinel on parse failure so malformed entries sort last
// without altering the error's intent.
func lastOctet(s string) int {
	if i := strings.Index(s, "="); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "."); i >= 0 {
		if n, err := strconv.Atoi(s[i+1:]); err == nil {
			return n
		}
	}
	return 1 << 30
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
