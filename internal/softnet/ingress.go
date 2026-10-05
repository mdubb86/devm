package softnet

import (
	"context"
	"fmt"
	"net"
	"sort"
	"sync"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"

	"github.com/mdubb86/devm/internal/helper"
	"github.com/mdubb86/devm/internal/identity"
)

// ingress manages host->guest port-forward listeners. apply() reconciles the
// live set against a daemon-pushed expose map: each host listener injects an
// accepted connection into the netstack toward GuestLeaseIP:GuestPort.
type ingress struct {
	n            *network
	helperClient *helper.Client
	mu           sync.Mutex
	listeners    map[int]*exposeListener // keyed by host port
}

type exposeListener struct {
	ln        net.Listener
	guestPort uint16
	bindIP    string
}

func newIngress(cfg identity.Config, n *network) *ingress {
	return &ingress{n: n, helperClient: helper.NewClient(cfg), listeners: map[int]*exposeListener{}}
}

// apply reconciles the listener set to exactly `ports` and returns the
// per-port outcome, sorted by host port. Bind failures are reported in
// the results (and acked back to the daemon) rather than crashing
// softnet — the daemon decides whether they're fatal.
func (ing *ingress) apply(ports []ExposePort) []ExposeResult {
	ing.mu.Lock()
	defer ing.mu.Unlock()
	want := map[int]ExposePort{}
	for _, p := range ports {
		if p.BindIP == "" {
			p.BindIP = HostLoopIP
		}
		want[p.HostPort] = p
	}
	// Close listeners no longer wanted, or whose guest port OR bind IP
	// changed. A stale bind IP is the dangerous case: the port would
	// keep answering on an address this project no longer owns.
	for hp, el := range ing.listeners {
		if w, ok := want[hp]; !ok || uint16(w.GuestPort) != el.guestPort || w.BindIP != el.bindIP {
			_ = el.ln.Close()
			logf("ingress close %s:%d (was -> guest:%d)", el.bindIP, hp, el.guestPort)
			delete(ing.listeners, hp)
		}
	}
	// Open newly-wanted listeners.
	results := make([]ExposeResult, 0, len(want))
	for hp, p := range want {
		res := ExposeResult{BindIP: p.BindIP, HostPort: hp, GuestPort: p.GuestPort, OK: true}
		if _, ok := ing.listeners[hp]; ok {
			results = append(results, res)
			continue
		}
		// Ports <1024 need root on macOS. softnet runs as an unprivileged
		// user process (spawned by `tart run --net-softnet` under the
		// daemon's identity), so a direct net.Listen on a low port fails
		// with EACCES. Route those through the root helper,
		// which pre-binds low ports on the devm pool IPs and hands back
		// the FD over a UDS. High ports still bind directly — no need to
		// round-trip through the helper for those.
		var ln net.Listener
		var err error
		if hp < 1024 {
			ln, err = ing.helperClient.BindTCP(p.BindIP, hp)
		} else {
			ln, err = net.Listen("tcp", net.JoinHostPort(p.BindIP, fmt.Sprint(hp)))
		}
		if err != nil {
			logf("ingress listen %s:%d: %v", p.BindIP, hp, err)
			res.OK = false
			res.Error = err.Error()
			results = append(results, res)
			continue
		}
		el := &exposeListener{ln: ln, guestPort: uint16(p.GuestPort), bindIP: p.BindIP}
		ing.listeners[hp] = el
		bindMode := "direct"
		if hp < 1024 {
			bindMode = "fd-passed"
		}
		logf("ingress open %s:%d -> guest:%d (%s)", p.BindIP, hp, p.GuestPort, bindMode)
		go ing.accept(el)
		results = append(results, res)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].HostPort < results[j].HostPort })
	return results
}

func (ing *ingress) accept(el *exposeListener) {
	for {
		hc, err := el.ln.Accept()
		if err != nil {
			return
		}
		go ing.forward(hc, el.guestPort)
	}
}

func (ing *ingress) forward(hc net.Conn, guestPort uint16) {
	if ing.n == nil {
		_ = hc.Close()
		return
	}
	hostAddr := hc.RemoteAddr().String()
	tAccept := time.Now()
	// FIONREAD at accept proves whether client bytes had already
	// arrived in the kernel receive buffer by the time this goroutine
	// started. If splice later reports h2g=0 with no error AND pending
	// was >0 here, the bytes were race-dropped between the host
	// kernel and io.Copy's first Read.
	pending := hostRecvQueueBytes(hc)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tDial := time.Now()
	gc, err := gonet.DialContextTCP(ctx, ing.n.stack, tcpip.FullAddress{
		NIC:  1,
		Addr: tcpip.AddrFrom4Slice(net.ParseIP(GuestLeaseIP).To4()),
		Port: guestPort,
	}, ipv4.ProtocolNumber)
	if err != nil {
		logf("ingress dial guest:%d: %v (accept->fail=%s)", guestPort, err, time.Since(tDial))
		_ = hc.Close()
		return
	}
	dialDur := time.Since(tDial)

	tSplice := time.Now()
	h2g, g2h, hErr, gErr := splice(hc, gc)
	spliceDur := time.Since(tSplice)

	// Race alarm: client wrote bytes that reached the kernel before we
	// started forwarding, yet splice saw zero host->guest bytes and no
	// error. This is the exact SSH "connection reset at kex" shape.
	if pending > 0 && h2g == 0 && realErr(hErr) == nil {
		logf("ALERT ingress race %s -> guest:%d: %d bytes pending at accept, splice h2g=0 clean EOF",
			hostAddr, guestPort, pending)
	}

	logf("ingress %s -> guest:%d ended h2g=%d g2h=%d pending=%d dial=%s splice=%s total=%s h2g_err=%v g2h_err=%v",
		hostAddr, guestPort, h2g, g2h, pending, dialDur, spliceDur, time.Since(tAccept), realErr(hErr), realErr(gErr))
}

func (ing *ingress) close() {
	ing.mu.Lock()
	defer ing.mu.Unlock()
	for hp, el := range ing.listeners {
		_ = el.ln.Close()
		logf("ingress close host:%d (shutdown)", hp)
		delete(ing.listeners, hp)
	}
}
