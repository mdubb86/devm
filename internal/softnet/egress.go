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
	ip  *IronProxyEndpoint
}

func newEgress(n *network) *egress { return &egress{n: n, pol: PolicyLocked} }

func (e *egress) setPolicy(p Policy, ip *IronProxyEndpoint) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pol = p
	if ip != nil {
		e.ip = ip
	}
}

// snapshot returns the current policy and iron-proxy endpoint under e.mu, for
// readers (e.g. target, startDNS's policyResolver) that need a consistent
// pair without holding the lock across their own work.
func (e *egress) snapshot() (Policy, *IronProxyEndpoint) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.pol, e.ip
}

// target maps an outbound TCP flow to a host dial address per current policy.
// ok=false => RST the flow. Pure; unit-tested.
func (e *egress) target(dstIP string, dport uint16) (string, bool) {
	pol, ip := e.snapshot()
	if dstIP == NATAliasIP {
		dstIP = HostLoopIP
	}
	switch pol {
	case PolicyOpen:
		return fmt.Sprintf("%s:%d", dstIP, dport), true
	case PolicyEnforced:
		if ip == nil {
			return "", false
		}
		switch dport {
		case 80:
			return ip.HTTP, true
		case 443:
			return ip.HTTPS, true
		}
		return "", false
	default: // LOCKED
		return "", false
	}
}

// udpTarget maps an outbound UDP flow to a host dial address per current
// policy. Mirrors target() but only NTP (:123) is forwarded when ENFORCED;
// DNS is served by a bound gateway:53 endpoint, not here. ok=false => drop.
func (e *egress) udpTarget(dstIP string, dport uint16) (string, bool) {
	pol, ip := e.snapshot()
	if dstIP == NATAliasIP {
		dstIP = HostLoopIP
	}
	switch pol {
	case PolicyOpen:
		return fmt.Sprintf("%s:%d", dstIP, dport), true
	case PolicyEnforced:
		if dport == 123 && ip != nil && ip.NTP != "" {
			return ip.NTP, true
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
