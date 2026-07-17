package softnet

import (
	"net"
	"sync"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

// attachUDP installs the policy UDP forwarder onto the stack. Each request is
// gated by e.udpTarget(...); ENFORCED only ever admits udp:123 (NTP). DNS is
// served by a bound gateway:53 endpoint (startDNS), which takes demux
// precedence over this forwarder handler, so the two coexist.
func attachUDP(n *network, e *egress) {
	fwd := udp.NewForwarder(n.stack, func(r *udp.ForwarderRequest) {
		id := r.ID()
		target, ok := e.udpTarget(id.LocalAddress.String(), id.LocalPort)
		if !ok {
			return
		}
		var wq waiter.Queue
		ep, terr := r.CreateEndpoint(&wq)
		if terr != nil {
			return
		}
		guestConn := gonet.NewUDPConn(&wq, ep)
		hostConn, err := net.Dial("udp", target)
		if err != nil {
			_ = guestConn.Close()
			return
		}
		go udpSplice(guestConn, hostConn)
	})
	n.stack.SetTransportProtocolHandler(udp.ProtocolNumber, fwd.HandlePacket)
}

// udpSplice copies datagrams both ways until 30s idle (UDP has no FIN; the
// idle timeout reaps the flow).
func udpSplice(guest, host net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		buf := make([]byte, 2048)
		for {
			_ = src.SetReadDeadline(time.Now().Add(30 * time.Second))
			n, err := src.Read(buf)
			if n > 0 {
				_, _ = dst.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}
	go cp(host, guest)
	go cp(guest, host)
	wg.Wait()
	_ = guest.Close()
	_ = host.Close()
}
