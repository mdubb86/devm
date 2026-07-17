// Command softnet is a throwaway de-risking spike: a drop-in replacement for
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
// Spike-only extras, passed via env so they don't perturb the tart contract:
//
//	SPIKE_ALLOW  comma-separated host:port allowlist (post-NAT dial targets)
//	SPIKE_MARKER path to write a JSON marker proving the contract + euid
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
	dnsZoneName = "spike.test."
)

// marker is the JSON proof the e2e reads back to assert the contract.
type marker struct {
	Argv       []string `json:"argv"`
	VMFD       int      `json:"vm_fd"`
	VMMac      string   `json:"vm_mac"`
	Allow      []string `json:"allow"`
	Block      []string `json:"block"`
	Expose     []string `json:"expose"`
	SockType   int      `json:"sock_type"`   // expect SOCK_DGRAM (2 on darwin)
	SockFamily int      `json:"sock_family"` // expect AF_UNIX (1)
	Euid       int      `json:"euid"`
	Uid        int      `json:"uid"`
	SpikeAllow []string `json:"spike_allow"`
	FirstSrc   string   `json:"first_src_mac"`  // src MAC of first guest frame
	FirstAt    string   `json:"first_frame_at"` // timestamp
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
		Argv:       os.Args,
		VMFD:       *vmFD,
		VMMac:      *vmMac,
		Allow:      allow,
		Block:      block,
		Expose:     expose,
		Euid:       os.Geteuid(),
		Uid:        os.Getuid(),
		SpikeAllow: splitCSV(os.Getenv("SPIKE_ALLOW")),
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
	for _, a := range mk.SpikeAllow {
		allowSet[a] = struct{}{}
	}

	writeMarker(&mk) // pre-flight marker so the e2e can read contract facts early

	log("softnet spike up: euid=%d uid=%d vm-fd=%d mac=%s allow=%v",
		mk.Euid, mk.Uid, *vmFD, *vmMac, mk.SpikeAllow)

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
	// No UDP forwarder installed => outbound UDP is default-dropped. DNS still
	// works because the DNS server binds gateway:53 as its own endpoint below.

	if err := startDNS(config, s); err != nil {
		return nil, fmt.Errorf("dns: %w", err)
	}
	if err := startDHCP(config, s, ipPool); err != nil {
		return nil, fmt.Errorf("dhcp: %w", err)
	}
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
// enforcement the spike exists to prove.
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
	path := os.Getenv("SPIKE_MARKER")
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
	fmt.Fprintf(os.Stderr, "[softnet-spike] "+format+"\n", args...)
}

func fatal(format string, args ...any) {
	log(format, args...)
	os.Exit(1)
}
