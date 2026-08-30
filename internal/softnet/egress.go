package softnet

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/waiter"
)

// keepAliveIdle / keepAliveInterval bound how long a stalled peer keeps a
// splice goroutine alive: gvisor's netstack default is 2h idle, which
// under high churn lets stuck goroutines accumulate faster than they
// clear (tailscale/tailscale#4522). One minute / fifteen seconds matches
// inetaf/tcpproxy's stdlib defaults and tailscale's netstack tuning.
const (
	keepAliveIdle     = 60 * time.Second
	keepAliveInterval = 15 * time.Second

	// egressLogWindow bounds how often an identical rejection or
	// dial failure re-logs. Chosen so a hot-loop guest under a
	// LOCKED policy doesn't drown the log while a persistent
	// misconfiguration still keeps a heartbeat once per minute.
	egressLogWindow = 60 * time.Second
)

// egressRejectLog and egressDialFailLog dedupe two silent-failure
// classes on the outbound path — policy rejects and dial failures.
// Both live at package scope so they persist across every forwarder
// invocation in a softnet process.
var (
	egressRejectLog   = newDedupLogger(egressLogWindow)
	egressDialFailLog = newDedupLogger(egressLogWindow)
)

// egress holds the current egress policy and decides, per outbound TCP flow,
// whether and where to forward it. setPolicy is called as the guest's state
// changes (boot lock -> forwarding); target is consulted per flow by the TCP
// forwarder installed by attachEgress.
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
// LOCKED drops everything; FORWARDING routes :80/:443 to iron-proxy (and the
// two hairpins below, regardless of policy). ok=false => RST the flow. Pure;
// unit-tested.
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
	// under FORWARDING regardless of the port arms below. Non-80/443 ports
	// are denied: direct services resolve to 127.0.0.1 and never reach this
	// address.
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

	// The daemon's per-project pop listener is reached at the gateway IP's
	// dedicated port, forwarded regardless of the policy switch below —
	// mirrors the .test hairpin's early decision above.
	if dstIP == GatewayIP && dport == 81 {
		if ft == nil {
			return "", false
		}
		return ft.Pop, ft.Pop != ""
	}

	switch pol {
	case PolicyForwarding:
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
// policy. Mirrors target() but only NTP (:123) is forwarded when FORWARDING;
// DNS is served by a bound gateway:53 endpoint, not here. ok=false => drop.
func (e *egress) udpTarget(dstIP string, dport uint16) (string, bool) {
	pol, ft := e.snapshot()
	if dstIP == NATAliasIP {
		dstIP = HostLoopIP
	}
	switch pol {
	case PolicyForwarding:
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
		dstIP := id.LocalAddress.String()
		host, ok := e.target(dstIP, id.LocalPort)
		if !ok {
			pol, _ := e.snapshot()
			egressRejectLog.Logf(
				fmt.Sprintf("%s:%d|%s", dstIP, id.LocalPort, pol),
				"egress reject %s:%d (policy=%s)", dstIP, id.LocalPort, pol,
			)
			r.Complete(true) // RST
			return
		}
		outbound, err := net.DialTimeout("tcp", host, 10*time.Second)
		if err != nil {
			egressDialFailLog.Logf(
				host,
				"egress dial %s -> %s failed: %v", dstIP, host, err,
			)
			r.Complete(true)
			return
		}
		enableStdlibKeepalive(outbound)
		var wq waiter.Queue
		ep, terr := r.CreateEndpoint(&wq)
		r.Complete(false)
		if terr != nil {
			_ = outbound.Close()
			return
		}
		enableGvisorKeepalive(ep)
		go func() {
			guestConn := gonet.NewTCPConn(&wq, ep)
			g2h, h2g, gErr, hErr := splice(guestConn, outbound)
			// Egress is high-volume: silent on healthy splices, log
			// only anomalies — a real error either direction, or a
			// splice that established but carried no bytes either way.
			gErr, hErr = realErr(gErr), realErr(hErr)
			if gErr != nil || hErr != nil || (g2h == 0 && h2g == 0) {
				logf("egress %s:%d -> %s ended g2h=%d h2g=%d g2h_err=%v h2g_err=%v",
					id.LocalAddress, id.LocalPort, host, g2h, h2g, gErr, hErr)
			}
		}()
	})
	n.stack.SetTransportProtocolHandler(tcp.ProtocolNumber, fwd.HandlePacket)
}

// splice bidirectionally copies bytes between a and b until both halves
// finish, then closes both ends. It returns the byte counts and error
// from each direction's io.Copy — aRead is what came out of a (forwarded
// a->b), bRead is what came out of b (forwarded b->a). Callers log these
// to surface splices that established but never carried data.
//
// Each direction pairs CloseWrite(dst) with CloseRead(src) when io.Copy
// returns — the CloseRead unblocks a peer that stopped reading its half
// while we were still writing, matching the pattern in inetaf/tcpproxy
// and tailscale's netstack forwarder. Both stdlib *net.TCPConn and
// gvisor gonet.TCPConn implement the two half-close methods.
func splice(a, b net.Conn) (aRead, bRead int64, aErr, bErr error) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		aRead, aErr = io.Copy(b, a)
		halfClose(b, a)
	}()
	go func() {
		defer wg.Done()
		bRead, bErr = io.Copy(a, b)
		halfClose(a, b)
	}()
	wg.Wait()
	_ = a.Close()
	_ = b.Close()
	return
}

// halfClose signals FIN on dst's write side and RST-if-buffered on
// src's read side, so a peer stalled mid-transfer unblocks.
func halfClose(dst, src net.Conn) {
	if c, ok := dst.(interface{ CloseWrite() error }); ok {
		_ = c.CloseWrite()
	}
	if c, ok := src.(interface{ CloseRead() error }); ok {
		_ = c.CloseRead()
	}
}

// realErr returns nil for the errors io.Copy normally reports on a
// clean end-of-stream (io.EOF, net.ErrClosed after our own Close, and
// the "use of closed network connection" text those wrap). Anything
// else is returned unchanged so it can drive the log gate.
func realErr(err error) error {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// enableStdlibKeepalive turns on TCP keepalive on a stdlib *net.TCPConn,
// so a dead host-side peer surfaces within ~keepAliveIdle instead of
// after the OS default (~2h on macOS). No-op on non-TCP conns.
func enableStdlibKeepalive(c net.Conn) {
	tc, ok := c.(*net.TCPConn)
	if !ok {
		return
	}
	_ = tc.SetKeepAlive(true)
	_ = tc.SetKeepAlivePeriod(keepAliveIdle)
}

// enableGvisorKeepalive turns on TCP keepalive on a gvisor endpoint,
// with the same idle / interval as the stdlib side. Gvisor's default
// idle is 2h; a stalled guest peer would otherwise pin a splice
// goroutine for that long (tailscale/tailscale#4522).
func enableGvisorKeepalive(ep tcpip.Endpoint) {
	ep.SocketOptions().SetKeepAlive(true)
	idle := tcpip.KeepaliveIdleOption(keepAliveIdle)
	interval := tcpip.KeepaliveIntervalOption(keepAliveInterval)
	_ = ep.SetSockOpt(&idle)
	_ = ep.SetSockOpt(&interval)
}
