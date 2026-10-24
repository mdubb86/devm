package serviceapi

import (
	"fmt"
	"sync"

	"github.com/mdubb86/devm/internal/daemonlog"
	"github.com/mdubb86/devm/internal/schema"
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
// of projectID just claimed. endpointFrom resolves each dependent's
// PeerHairpins live from the peers index, so the new ports show up
// in the dependent's forward targets automatically.
//
// A dependent with no projectInfo entry OR no softnet socket is
// skipped. A push failure is logged but does not fail the caller —
// the project whose rebind triggered this fanout still succeeded, and
// a dead dependent is a separate problem.
func propagatePeerRebind(projectID string, ntpPort int, tld string) {
	for _, dep := range peersState.dependentsOf(projectID) {
		depInfo, ok := ironProxyState.get(dep)
		if !ok {
			continue
		}
		depSock, ok := softnetState.get(dep)
		if !ok {
			continue
		}
		if err := newSoftnetClient(depSock).setPolicy("FORWARDING", endpointFrom(dep, depInfo, ntpPort, tld)); err != nil {
			daemonlog.Errorf("serviceapi: peer-rebind re-push for %s (dep of %s): %v", dep, projectID, err)
		}
	}
}

// projectIDsFromPeerServices extracts the Project field from each
// declared peer_service entry. Nil / zero entries are skipped — the
// schema validator rejects those at config load, but this helper runs
// defensively on the daemon side too.
func projectIDsFromPeerServices(ps []*schema.PeerService) []string {
	if len(ps) == 0 {
		return nil
	}
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		if p == nil || p.Project == "" {
			continue
		}
		out = append(out, p.Project)
	}
	return out
}

// macPortsFromServices extracts the Port field from each declared
// mac_service entry — the set softnet's egress forwarder will
// authorize for this VM via ForwardTargets.MacPorts. Nil / zero entries
// are skipped (same defensive stance as projectIDsFromPeerServices).
func macPortsFromServices(ms []*schema.MacService) []int {
	if len(ms) == 0 {
		return nil
	}
	out := make([]int, 0, len(ms))
	for _, m := range ms {
		if m == nil || m.Port == 0 {
			continue
		}
		out = append(out, m.Port)
	}
	return out
}

// validatePeerServicesCrossProject fails loud on two conditions the
// schema-level shape validator can't catch (it has no visibility into
// the set of registered projects):
//
//   - A self-peer (peer_services[].project equals the declaring
//     project's own name): a projectwide alias to itself with no
//     meaning, almost always a copy-paste error.
//   - An unknown peer (peer_services[].project not in known): the
//     reverse-hairpin would route to a `.test` zone no project owns.
//
// knownProjects is the daemon's current set of registered project IDs
// (whatever listing the orchestrator uses — KnownProjectNamesForWatchdog
// at /vm/start, same list via snap.Cfg at /vm/apply-iron-proxy). Nil /
// empty peer_services returns nil immediately so a project with no
// peers declared never trips the known-set check.
func validatePeerServicesCrossProject(selfProjectID string, ps []*schema.PeerService, knownProjects []string) error {
	if len(ps) == 0 {
		return nil
	}
	known := make(map[string]struct{}, len(knownProjects))
	for _, k := range knownProjects {
		known[k] = struct{}{}
	}
	// selfProjectID is always a legitimate target of the known-set check
	// (a project declaring itself exists even if its state snapshot was
	// written mid-start). Insert it so the only remaining rejection path
	// is the self-peer case below.
	known[selfProjectID] = struct{}{}
	for i, p := range ps {
		if p == nil || p.Project == "" {
			continue
		}
		if p.Project == selfProjectID {
			return fmt.Errorf("peer_services[%d].project %q: self-peer (a project cannot peer with itself)", i, p.Project)
		}
		if _, ok := known[p.Project]; !ok {
			return fmt.Errorf("peer_services[%d].project %q: unknown peer (no project with that name is registered on this daemon)", i, p.Project)
		}
	}
	return nil
}
