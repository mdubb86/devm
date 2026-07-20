package softnet

import (
	"context"
	"fmt"
	"net"
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
}

func newIngress(cfg identity.Config, n *network) *ingress {
	return &ingress{n: n, helperClient: helper.NewClient(cfg), listeners: map[int]*exposeListener{}}
}

// apply reconciles the listener set to exactly `ports`.
func (ing *ingress) apply(ports []ExposePort) {
	ing.mu.Lock()
	defer ing.mu.Unlock()
	want := map[int]ExposePort{}
	for _, p := range ports {
		want[p.HostPort] = p
	}
	// Close listeners no longer wanted (or whose guest port changed).
	for hp, el := range ing.listeners {
		if w, ok := want[hp]; !ok || uint16(w.GuestPort) != el.guestPort {
			_ = el.ln.Close()
			delete(ing.listeners, hp)
		}
	}
	// Open newly-wanted listeners.
	for hp, p := range want {
		if _, ok := ing.listeners[hp]; ok {
			continue
		}
		bind := p.BindIP
		if bind == "" {
			bind = HostLoopIP
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
			ln, err = ing.helperClient.BindTCP(bind, hp)
		} else {
			ln, err = net.Listen("tcp", net.JoinHostPort(bind, fmt.Sprint(hp)))
		}
		if err != nil {
			logf("ingress listen %s:%d: %v", bind, hp, err)
			continue // best-effort; a bind conflict shouldn't crash softnet
		}
		el := &exposeListener{ln: ln, guestPort: uint16(p.GuestPort)}
		ing.listeners[hp] = el
		go ing.accept(el)
	}
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	gc, err := gonet.DialContextTCP(ctx, ing.n.stack, tcpip.FullAddress{
		NIC:  1,
		Addr: tcpip.AddrFrom4Slice(net.ParseIP(GuestLeaseIP).To4()),
		Port: guestPort,
	}, ipv4.ProtocolNumber)
	if err != nil {
		logf("ingress dial guest:%d: %v", guestPort, err)
		_ = hc.Close()
		return
	}
	splice(hc, gc) // reuse egress.go's splice
}

func (ing *ingress) close() {
	ing.mu.Lock()
	defer ing.mu.Unlock()
	for hp, el := range ing.listeners {
		_ = el.ln.Close()
		delete(ing.listeners, hp)
	}
}
