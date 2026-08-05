package softnet

import (
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/waiter"
)

// egress holds the current egress policy and decides, per outbound TCP flow,
// whether and where to forward it. setPolicy is called as the guest's state
// changes (boot lock -> provisioning -> enforced); target is consulted per
// flow by the TCP forwarder installed by attachEgress.
type egress struct {
	n   *network
	mu  sync.RWMutex
	pol Policy
	ft  *ForwardTargets

	// directTest is the set of `direct: true` hostnames (no trailing dot).
	// The .test DNS intercept answers HostLoopIP for these — their traffic
	// stays in-guest, raw TCP end-to-end — and InterceptedTestIP for every
	// other .test name. Pushed by the daemon via the setTestHosts control op.
	directTest map[string]struct{}
}

func newEgress(n *network) *egress { return &egress{n: n, pol: PolicyLocked} }

func (e *egress) setPolicy(p Policy, ft *ForwardTargets) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pol = p
	if ft != nil {
		e.ft = ft
	}
}

// snapshot returns the current policy and forward targets under e.mu, for
// readers (e.g. target, startDNS's policyResolver) that need a consistent
// pair without holding the lock across their own work.
func (e *egress) snapshot() (Policy, *ForwardTargets) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.pol, e.ft
}

// setDirectTestHosts replaces the direct-hostname set. Replace, not merge:
// a removed direct service must stop answering loopback on the next push.
func (e *egress) setDirectTestHosts(hosts []string) {
	m := make(map[string]struct{}, len(hosts))
	for _, h := range hosts {
		m[h] = struct{}{}
	}
	e.mu.Lock()
	e.directTest = m
	e.mu.Unlock()
}

// testAnswer resolves a .test FQDN against the current direct set.
func (e *egress) testAnswer(fqdn string) (net.IP, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return testAnswerIP(fqdn, e.directTest)
}

// target maps an outbound TCP flow to a host dial address per current policy.
// ok=false => RST the flow. Pure; unit-tested.
func (e *egress) target(dstIP string, dport uint16) (string, bool) {
	pol, ft := e.snapshot()
	if dstIP == NATAliasIP {
		dstIP = HostLoopIP
	}
	if pol == PolicyLocked {
		return "", false
	}

	// `.test` loops back into this project's own services via the daemon's
	// guest-origin listener. Decided ahead of the policy switch so it works
	// during the provisioning window (OPEN) as well as under ENFORCED.
	// Non-80/443 ports are denied: direct services resolve to 127.0.0.1 and
	// never reach this address.
	if dstIP == InterceptedTestIP {
		if ft == nil {
			return "", false
		}
		switch dport {
		case 80:
			return ft.GuestHTTP, ft.GuestHTTP != ""
		case 443:
			return ft.GuestHTTPS, ft.GuestHTTPS != ""
		}
		return "", false
	}

	switch pol {
	case PolicyOpen:
		return fmt.Sprintf("%s:%d", dstIP, dport), true
	case PolicyEnforced:
		if ft == nil {
			return "", false
		}
		switch dport {
		case 80:
			return ft.HTTP, ft.HTTP != ""
		case 443:
			return ft.HTTPS, ft.HTTPS != ""
		}
		return "", false
	default:
		return "", false
	}
}

// udpTarget maps an outbound UDP flow to a host dial address per current
// policy. Mirrors target() but only NTP (:123) is forwarded when ENFORCED;
// DNS is served by a bound gateway:53 endpoint, not here. ok=false => drop.
func (e *egress) udpTarget(dstIP string, dport uint16) (string, bool) {
	pol, ft := e.snapshot()
	if dstIP == NATAliasIP {
		dstIP = HostLoopIP
	}
	switch pol {
	case PolicyOpen:
		return fmt.Sprintf("%s:%d", dstIP, dport), true
	case PolicyEnforced:
		if dport == 123 && ft != nil && ft.NTP != "" {
			return ft.NTP, true
		}
		return "", false
	default: // LOCKED
		return "", false
	}
}

// attachEgress installs the policy TCP forwarder onto the stack. The forwarder
// body is ported from the fixture's policyTCPForwarder, replacing the allowSet
// lookup with e.target(...).
func attachEgress(n *network, e *egress) {
	fwd := tcp.NewForwarder(n.stack, 0, 100, func(r *tcp.ForwarderRequest) {
		id := r.ID()
		host, ok := e.target(id.LocalAddress.String(), id.LocalPort)
		if !ok {
			r.Complete(true) // RST
			return
		}
		outbound, err := net.DialTimeout("tcp", host, 10*time.Second)
		if err != nil {
			r.Complete(true)
			return
		}
		var wq waiter.Queue
		ep, terr := r.CreateEndpoint(&wq)
		r.Complete(false)
		if terr != nil {
			_ = outbound.Close()
			return
		}
		go splice(gonet.NewTCPConn(&wq, ep), outbound)
	})
	n.stack.SetTransportProtocolHandler(tcp.ProtocolNumber, fwd.HandlePacket)
}

// splice — ported verbatim from the fixture.
func splice(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if c, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = c.CloseWrite()
		}
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
	_ = a.Close()
	_ = b.Close()
}
