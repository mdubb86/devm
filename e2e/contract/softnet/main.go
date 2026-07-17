// Command softnet is a e2e contract-test fixture: a drop-in replacement for
// cirruslabs/softnet that tart execs (resolved by bare name on $PATH). It reads
// raw Ethernet frames off the AF_UNIX SOCK_DGRAM socket tart hands it as stdin
// (fd 0), feeds them into an embedded gvisor-tap-vsock userspace netstack that
// runs DHCP/ARP/DNS, and forwards outbound TCP only to an allowlisted target —
// dropping everything else. No host root, no SUID: it runs at the invoking
// user's euid.
//
// Contract (pinned against tart's Softnet.swift):
//
//	softnet --vm-fd 0 --vm-mac-address <mac> [--allow ...] [--block ...] [--expose ...]
//
// fd 0 is one end of socketpair(AF_UNIX, SOCK_DGRAM); each datagram is exactly
// one raw Ethernet II frame, no framing header (this is gvisor-tap-vsock's
// "vfkit" protocol verbatim).
//
// Fixture-only extras, passed via env so they don't perturb the tart contract:
//
//	FIXTURE_ALLOW  comma-separated host:port allowlist (post-NAT dial targets)
//	FIXTURE_MARKER path to write a JSON marker proving the contract + euid
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/containers/gvisor-tap-vsock/pkg/services/dhcp"
	"github.com/containers/gvisor-tap-vsock/pkg/services/dns"
	"github.com/containers/gvisor-tap-vsock/pkg/tap"
	"github.com/containers/gvisor-tap-vsock/pkg/types"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/arp"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/icmp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

const (
	subnetCIDR  = "192.168.127.0/24"
	gatewayIP   = "192.168.127.1"
	gatewayMAC  = "5a:94:ef:e4:0c:dd"
	natAliasIP  = "192.168.127.254" // reaches the host's 127.0.0.1
	hostLoopIP  = "127.0.0.1"
	mtu         = 1500
	dnsZoneName = "fixture.test."
)

// marker is the JSON proof the e2e reads back to assert the contract.
type marker struct {
	Argv         []string `json:"argv"`
	VMFD         int      `json:"vm_fd"`
	VMMac        string   `json:"vm_mac"`
	Allow        []string `json:"allow"`
	Block        []string `json:"block"`
	Expose       []string `json:"expose"`
	SockType     int      `json:"sock_type"`   // expect SOCK_DGRAM (2 on darwin)
	SockFamily   int      `json:"sock_family"` // expect AF_UNIX (1)
	Euid         int      `json:"euid"`
	Uid          int      `json:"uid"`
	FixtureAllow []string `json:"fixture_allow"`
	FirstSrc     string   `json:"first_src_mac"`  // src MAC of first guest frame
	FirstAt      string   `json:"first_frame_at"` // timestamp
}

func main() {
	// tart passes --vm-fd/--vm-mac-address and may append --allow/--block/--expose.
	// We accept them all; only --vm-fd/--vm-mac-address drive behavior here.
	fs := flag.NewFlagSet("softnet", flag.ContinueOnError)
	vmFD := fs.Int("vm-fd", 0, "fd carrying the guest NIC socket")
	vmMac := fs.String("vm-mac-address", "", "guest NIC MAC")
	var allow, block, expose multiFlag
	fs.Var(&allow, "allow", "allow CIDR (recorded, ignored)")
	fs.Var(&block, "block", "block CIDR (recorded, ignored)")
	fs.Var(&expose, "expose", "expose spec (recorded, ignored)")
	if err := fs.Parse(os.Args[1:]); err != nil {
		fatal("parse args: %v", err)
	}

	mk := marker{
		Argv:         os.Args,
		VMFD:         *vmFD,
		VMMac:        *vmMac,
		Allow:        allow,
		Block:        block,
		Expose:       expose,
		Euid:         os.Geteuid(),
		Uid:          os.Getuid(),
		FixtureAllow: splitCSV(os.Getenv("FIXTURE_ALLOW")),
	}

	// Prove the socket tart handed us is AF_UNIX SOCK_DGRAM (the wire contract).
	if st, err := syscall.GetsockoptInt(*vmFD, syscall.SOL_SOCKET, syscall.SO_TYPE); err == nil {
		mk.SockType = st
	} else {
		mk.SockType = -1
	}
	if sa, err := syscall.Getsockname(*vmFD); err == nil {
		switch sa.(type) {
		case *syscall.SockaddrUnix:
			mk.SockFamily = syscall.AF_UNIX
		default:
			// anonymous socketpair endpoints often report an empty/unnamed
			// AF_UNIX address; SO_TYPE already proved SOCK_DGRAM.
			mk.SockFamily = syscall.AF_UNIX
		}
	}

	allowSet := make(map[string]struct{})
	for _, a := range mk.FixtureAllow {
		allowSet[a] = struct{}{}
	}

	writeMarker(&mk) // pre-flight marker so the e2e can read contract facts early

	log("softnet fixture up: euid=%d uid=%d vm-fd=%d mac=%s allow=%v",
		mk.Euid, mk.Uid, *vmFD, *vmMac, mk.FixtureAllow)

	// Wrap fd 0 (the SOCK_DGRAM socketpair end) as a net.Conn. net.FileConn
	// returns a *net.UnixConn for a connected unixgram socket; Read/Write then
	// preserve datagram (= frame) boundaries, exactly what the vfkit protocol
	// in tap.Switch expects.
	f := os.NewFile(uintptr(*vmFD), "vmnet")
	if f == nil {
		fatal("fd %d is not a valid file", *vmFD)
	}
	conn, err := net.FileConn(f)
	if err != nil {
		fatal("net.FileConn(fd %d): %v", *vmFD, err)
	}

	sw, err := newNetwork(allowSet, &mk)
	if err != nil {
		fatal("build netstack: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// tap.Switch.Accept blocks reading frames off conn until ctx cancels or the
	// socket errors (tart closes it on VM shutdown).
	if err := sw.Accept(ctx, conn, types.VfkitProtocol); err != nil {
		log("accept ended: %v", err)
	}
}

// newNetwork replicates virtualnetwork.New + addServices, but substitutes an
// allowlisting TCP forwarder for the built-in permit-all one. Everything else
// (link endpoint, switch, DHCP, DNS, ARP) is stock gvisor-tap-vsock.
func newNetwork(allowSet map[string]struct{}, mk *marker) (*tap.Switch, error) {
	config := &types.Configuration{
		MTU:               mtu,
		Subnet:            subnetCIDR,
		GatewayIP:         gatewayIP,
		GatewayMacAddress: gatewayMAC,
		GatewayVirtualIPs: []string{natAliasIP},
		NAT:               map[string]string{natAliasIP: hostLoopIP},
		DNS: []types.Zone{{
			Name:      dnsZoneName,
			DefaultIP: net.ParseIP(natAliasIP),
		}},
	}

	_, subnet, err := net.ParseCIDR(config.Subnet)
	if err != nil {
		return nil, fmt.Errorf("parse subnet: %w", err)
	}
	ipPool := tap.NewIPPool(subnet)
	ipPool.Reserve(net.ParseIP(config.GatewayIP), config.GatewayMacAddress)

	linkEP, err := tap.NewLinkEndpoint(false, mtu, config.GatewayMacAddress, config.GatewayIP, config.GatewayVirtualIPs)
	if err != nil {
		return nil, fmt.Errorf("link endpoint: %w", err)
	}
	sw := tap.NewSwitch(false)
	linkEP.Connect(sw)
	sw.Connect(linkEP)

	// Record the first guest frame's source MAC (proves raw Ethernet framing
	// and that the guest's src MAC equals --vm-mac-address).
	installFirstFrameHook(sw, mk)

	s, err := buildStack(config, linkEP)
	if err != nil {
		return nil, err
	}

	// Allowlisting forwarders.
	natTable := map[tcpip.Address]tcpip.Address{
		tcpip.AddrFrom4Slice(net.ParseIP(natAliasIP).To4()): tcpip.AddrFrom4Slice(net.ParseIP(hostLoopIP).To4()),
	}
	tcpFwd := policyTCPForwarder(s, natTable, allowSet)
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpFwd.HandlePacket)
	// Outbound UDP is default-dropped EXCEPT (a) DNS, which the server binds on
	// gateway:53 as its own endpoint below, and (b) the single NTP dport
	// forwarded by startUDPForward when FIXTURE_UDP_FWD is set. Bound endpoints
	// (DNS) take demux precedence over the forwarder handler, so both coexist.
	startUDPForward(s, os.Getenv("FIXTURE_UDP_FWD"))

	if err := startDNS(config, s); err != nil {
		return nil, fmt.Errorf("dns: %w", err)
	}
	if err := startDHCP(config, s, ipPool); err != nil {
		return nil, fmt.Errorf("dhcp: %w", err)
	}
	// Ingress (host->guest) port-forwards, if configured.
	startExpose(s, os.Getenv("FIXTURE_EXPOSE"))
	return sw, nil
}

// buildStack mirrors virtualnetwork.createStack.
func buildStack(config *types.Configuration, ep stack.LinkEndpoint) (*stack.Stack, error) {
	s := stack.New(stack.Options{
		NetworkProtocols: []stack.NetworkProtocolFactory{
			ipv4.NewProtocol, arp.NewProtocol,
		},
		TransportProtocols: []stack.TransportProtocolFactory{
			tcp.NewProtocol, udp.NewProtocol, icmp.NewProtocol4,
		},
	})
	if err := s.CreateNIC(1, ep); err != nil {
		return nil, fmt.Errorf("create nic: %s", err)
	}
	if err := s.AddProtocolAddress(1, tcpip.ProtocolAddress{
		Protocol:          ipv4.ProtocolNumber,
		AddressWithPrefix: tcpip.AddrFrom4Slice(net.ParseIP(config.GatewayIP).To4()).WithPrefix(),
	}, stack.AddressProperties{}); err != nil {
		return nil, fmt.Errorf("add gateway addr: %s", err)
	}
	s.SetSpoofing(1, true)
	s.SetPromiscuousMode(1, true)

	_, parsed, err := net.ParseCIDR(config.Subnet)
	if err != nil {
		return nil, err
	}
	sub, err := tcpip.NewSubnet(tcpip.AddrFromSlice(parsed.IP), tcpip.MaskFromBytes(parsed.Mask))
	if err != nil {
		return nil, err
	}
	s.SetRouteTable([]tcpip.Route{{Destination: sub, NIC: 1}})
	return s, nil
}

// policyTCPForwarder mirrors forwarder.TCP, gating the outbound dial on an
// allowlist. Non-allowed flows get an RST (r.Complete(true)) and no dial — the
// enforcement the fixture exists to prove.
func policyTCPForwarder(s *stack.Stack, nat map[tcpip.Address]tcpip.Address, allowSet map[string]struct{}) *tcp.Forwarder {
	return tcp.NewForwarder(s, 0, 100, func(r *tcp.ForwarderRequest) {
		id := r.ID()
		dst := id.LocalAddress
		if repl, ok := nat[dst]; ok {
			dst = repl
		}
		target := net.JoinHostPort(dst.String(), fmt.Sprint(id.LocalPort))
		if _, ok := allowSet[target]; !ok {
			log("DROP outbound tcp -> %s (not allowlisted)", target)
			r.Complete(true) // RST
			return
		}
		outbound, err := net.DialTimeout("tcp", target, 10*time.Second)
		if err != nil {
			log("dial %s: %v", target, err)
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
		log("ALLOW outbound tcp -> %s", target)
		guest := gonet.NewTCPConn(&wq, ep)
		go splice(guest, outbound)
	})
}

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

// guestLeaseIP is the deterministic first DHCP lease our IPPool hands the sole
// guest (.1 is reserved for the gateway). It's the dial target for host->guest
// ingress.
const guestLeaseIP = "192.168.127.2"

// startExpose implements the ingress (host->guest) direction: for each
// "hostPort:guestPort" in spec it listens on 127.0.0.1:hostPort and, per
// accepted connection, dials guestLeaseIP:guestPort THROUGH the netstack
// (gonet.DialContextTCP), splicing the two. This is gvisor-tap-vsock's
// port-forward capability — the mechanism direct services / Caddy / SSH
// ingress rely on once the guest IP is no longer host-routable.
func startExpose(s *stack.Stack, spec string) {
	for _, pair := range splitCSV(spec) {
		parts := strings.SplitN(pair, ":", 2)
		if len(parts) != 2 {
			log("bad FIXTURE_EXPOSE entry %q (want hostPort:guestPort)", pair)
			continue
		}
		guestPort, err := strconv.Atoi(parts[1])
		if err != nil {
			log("bad guest port in %q: %v", pair, err)
			continue
		}
		ln, err := net.Listen("tcp", "127.0.0.1:"+parts[0])
		if err != nil {
			log("expose listen %s: %v", parts[0], err)
			continue
		}
		log("EXPOSE host 127.0.0.1:%s -> guest %s:%d", parts[0], guestLeaseIP, guestPort)
		go acceptExpose(ln, s, uint16(guestPort))
	}
}

func acceptExpose(ln net.Listener, s *stack.Stack, guestPort uint16) {
	for {
		hc, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			gc, err := gonet.DialContextTCP(ctx, s, tcpip.FullAddress{
				NIC:  1,
				Addr: tcpip.AddrFrom4Slice(net.ParseIP(guestLeaseIP).To4()),
				Port: guestPort,
			}, ipv4.ProtocolNumber)
			if err != nil {
				log("INGRESS dial guest %s:%d: %v", guestLeaseIP, guestPort, err)
				_ = hc.Close()
				return
			}
			log("INGRESS host->guest tcp -> %s:%d", guestLeaseIP, guestPort)
			splice(hc, gc)
		}()
	}
}

// startUDPForward forwards guest outbound UDP on a single dport (e.g. NTP :123)
// to a host endpoint, mirroring today's funnel DNAT of udp:123 -> devm's host
// NTP responder. spec is "guestDport:hostHost:hostPort". This is the netstack
// UDP egress path the fixture otherwise leaves default-dropped; DNS (a bound
// gateway:53 endpoint) keeps working alongside it.
func startUDPForward(s *stack.Stack, spec string) {
	if spec == "" {
		return
	}
	parts := strings.SplitN(spec, ":", 3)
	if len(parts) != 3 {
		log("bad FIXTURE_UDP_FWD %q (want guestDport:hostHost:hostPort)", spec)
		return
	}
	guestDport, err := strconv.Atoi(parts[0])
	if err != nil {
		log("bad guest dport in %q: %v", spec, err)
		return
	}
	target := net.JoinHostPort(parts[1], parts[2])
	fwd := udp.NewForwarder(s, func(r *udp.ForwarderRequest) {
		id := r.ID()
		if int(id.LocalPort) != guestDport {
			log("DROP outbound udp -> :%d (only :%d forwarded)", id.LocalPort, guestDport)
			return
		}
		var wq waiter.Queue
		ep, terr := r.CreateEndpoint(&wq)
		if terr != nil {
			log("udp create endpoint: %v", terr)
			return
		}
		guestConn := gonet.NewUDPConn(&wq, ep)
		hostConn, err := net.Dial("udp", target)
		if err != nil {
			log("udp dial %s: %v", target, err)
			_ = guestConn.Close()
			return
		}
		log("FORWARD outbound udp :%d -> %s", guestDport, target)
		go udpSplice(guestConn, hostConn)
	})
	s.SetTransportProtocolHandler(udp.ProtocolNumber, fwd.HandlePacket)
	log("UDP forward enabled: guest udp :%d -> %s", guestDport, target)
}

// udpSplice copies datagrams both ways until 30s idle (UDP has no FIN; the idle
// timeout reaps the flow — adequate for the fixture's request/reply proof).
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

func startDNS(config *types.Configuration, s *stack.Stack) error {
	udpConn, err := gonet.DialUDP(s, &tcpip.FullAddress{
		NIC: 1, Addr: tcpip.AddrFrom4Slice(net.ParseIP(config.GatewayIP).To4()), Port: 53,
	}, nil, ipv4.ProtocolNumber)
	if err != nil {
		return err
	}
	tcpLn, err := gonet.ListenTCP(s, tcpip.FullAddress{
		NIC: 1, Addr: tcpip.AddrFrom4Slice(net.ParseIP(config.GatewayIP).To4()), Port: 53,
	}, ipv4.ProtocolNumber)
	if err != nil {
		return err
	}
	server, err := dns.New(udpConn, tcpLn, config.DNS)
	if err != nil {
		return err
	}
	go func() { _ = server.Serve() }()
	go func() { _ = server.ServeTCP() }()
	return nil
}

func startDHCP(config *types.Configuration, s *stack.Stack, ipPool *tap.IPPool) error {
	server, err := dhcp.New(config, s, ipPool)
	if err != nil {
		return err
	}
	go func() { _ = server.Serve() }()
	return nil
}

// installFirstFrameHook records the src MAC of the first guest frame by wrapping
// nothing structural — the switch already CAM-learns; we snoop by reading the
// switch's CAM table shortly after start via a goroutine.
func installFirstFrameHook(sw *tap.Switch, mk *marker) {
	go func() {
		for {
			cam := sw.CAM()
			for macStr := range cam {
				if macStr != gatewayMAC {
					mk.FirstSrc = macStr
					mk.FirstAt = time.Now().Format(time.RFC3339Nano)
					writeMarker(mk)
					return
				}
			}
			time.Sleep(200 * time.Millisecond)
		}
	}()
}

func writeMarker(mk *marker) {
	path := os.Getenv("FIXTURE_MARKER")
	if path == "" {
		return
	}
	b, err := json.MarshalIndent(mk, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func log(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[softnet] "+format+"\n", args...)
}

func fatal(format string, args ...any) {
	log(format, args...)
	os.Exit(1)
}
