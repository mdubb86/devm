package serviceapi

import (
	"fmt"
	"sync"

	"github.com/mdubb86/devm/internal/daemonlog"
)

// peersIndex tracks the directed declared-peer graph between projects:
// `peersOf[projectID]` is the set of peer project IDs projectID has
// declared, and `deps[peerID]` is the inverse — the set of projects
// that list peerID. Both directions are maintained together so the
// propagation path (dependentsOf) is a single map lookup instead of a
// full scan.
//
// In-memory only, mutex-guarded, mirroring projectInfoStore's pattern.
// A daemon restart rebuilds it from each project's current config.
type peersIndex struct {
	mu      sync.Mutex
	peersOf map[string]map[string]struct{}
	deps    map[string]map[string]struct{}
}

func newPeersIndex() *peersIndex {
	return &peersIndex{
		peersOf: map[string]map[string]struct{}{},
		deps:    map[string]map[string]struct{}{},
	}
}

// setPeers replaces projectID's declared peers with the given set.
// Both directions (peersOf and deps) are reconciled: old edges from
// projectID are removed from the inverse map before the new ones are
// recorded.
func (p *peersIndex) setPeers(projectID string, peers []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for peer := range p.peersOf[projectID] {
		if d, ok := p.deps[peer]; ok {
			delete(d, projectID)
			if len(d) == 0 {
				delete(p.deps, peer)
			}
		}
	}
	set := map[string]struct{}{}
	for _, pr := range peers {
		set[pr] = struct{}{}
	}
	p.peersOf[projectID] = set
	for peer := range set {
		if _, ok := p.deps[peer]; !ok {
			p.deps[peer] = map[string]struct{}{}
		}
		p.deps[peer][projectID] = struct{}{}
	}
}

// remove drops projectID from the forward map (peersOf) on teardown.
// Edges where projectID appears as someone else's peer stay — those
// dependents still need to know when projectID comes back.
func (p *peersIndex) remove(projectID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for peer := range p.peersOf[projectID] {
		if d, ok := p.deps[peer]; ok {
			delete(d, projectID)
			if len(d) == 0 {
				delete(p.deps, peer)
			}
		}
	}
	delete(p.peersOf, projectID)
}

// dependentsOf returns every project that lists peerID as a peer, as a
// snapshot slice. The lock is released before the caller iterates so
// per-dependent network IO can run without blocking other peers index
// mutations.
func (p *peersIndex) dependentsOf(peerID string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.deps[peerID]))
	for id := range p.deps[peerID] {
		out = append(out, id)
	}
	return out
}

// peerBackends is the shared body of peerHairpinsFor and
// peerHairpinsHTTPFor: build `<peer>.<tld> → "127.0.0.1:<port>"` from
// projectID's declared peers, reading each peer's port via portOf
// against the live projectInfoStore. A peer not registered in the
// store OR whose portOf returns zero is omitted — the SNI/Host mux
// treats a missing entry as fail-closed (unrecognized name) rather
// than dialing a known-bad target.
func (p *peersIndex) peerBackends(projectID string, s *projectInfoStore, tld string, portOf func(projectInfo) int) map[string]string {
	p.mu.Lock()
	peers := make([]string, 0, len(p.peersOf[projectID]))
	for peer := range p.peersOf[projectID] {
		peers = append(peers, peer)
	}
	p.mu.Unlock()
	out := map[string]string{}
	for _, peer := range peers {
		info, ok := s.get(peer)
		if !ok {
			continue
		}
		port := portOf(info)
		if port == 0 {
			continue
		}
		out[peer+"."+tld] = fmt.Sprintf("127.0.0.1:%d", port)
	}
	return out
}

// peerHairpinsFor returns the live HTTPS backend map for projectID:
// `<peer>.<tld> → "127.0.0.1:<GuestHTTPSPort>"`, one entry per
// declared peer whose guest-origin listener pair is currently bound.
// tld is the daemon's TLD (prod: "test"; e2e: "e2e.test") — hardcoding
// it would produce wrong keys in the e2e slot.
func (p *peersIndex) peerHairpinsFor(projectID string, s *projectInfoStore, tld string) map[string]string {
	return p.peerBackends(projectID, s, tld, func(i projectInfo) int { return i.GuestHTTPSPort })
}

// peerHairpinsHTTPFor is the plain-HTTP sibling of peerHairpinsFor:
// same keys, values pointing at each peer's GuestHTTPPort.
func (p *peersIndex) peerHairpinsHTTPFor(projectID string, s *projectInfoStore, tld string) map[string]string {
	return p.peerBackends(projectID, s, tld, func(i projectInfo) int { return i.GuestHTTPPort })
}

var peersState = newPeersIndex()

// propagatePeerRebind re-pushes each dependent's current setPolicy
// envelope to its softnet so softnet learns the fresh ports a rebind
// of projectID just claimed. Does not yet carry per-dependent
// peer-hairpins — endpointFrom gains that in Task 9; this fan-out is
// the trigger it needs.
//
// A dependent with no projectInfo entry OR no softnet socket is
// skipped. A push failure is logged but does not fail the caller —
// the project whose rebind triggered this fanout still succeeded, and
// a dead dependent is a separate problem.
func propagatePeerRebind(projectID string, ntpPort int) {
	for _, dep := range peersState.dependentsOf(projectID) {
		depInfo, ok := ironProxyState.get(dep)
		if !ok {
			continue
		}
		depSock, ok := softnetState.get(dep)
		if !ok {
			continue
		}
		if err := newSoftnetClient(depSock).setPolicy("FORWARDING", endpointFrom(depInfo, ntpPort)); err != nil {
			daemonlog.Errorf("serviceapi: peer-rebind re-push for %s (dep of %s): %v", dep, projectID, err)
		}
	}
}
