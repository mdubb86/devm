package main

import (
	"context"
	"encoding/binary"
	"net"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
)

// socketpairConns mirrors tart's socketpair(AF_UNIX, SOCK_DGRAM): one end is
// wrapped as the "guest" conn, the other handed to the softnet netstack.
func socketpairConns(t *testing.T) (guest net.Conn, softnet net.Conn) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	gf := os.NewFile(uintptr(fds[0]), "guest")
	sf := os.NewFile(uintptr(fds[1]), "softnet")
	gc, err := net.FileConn(gf)
	if err != nil {
		t.Fatalf("FileConn guest: %v", err)
	}
	sc, err := net.FileConn(sf)
	if err != nil {
		t.Fatalf("FileConn softnet: %v", err)
	}
	_ = gf.Close()
	_ = sf.Close()
	return gc, sc
}

func startNetwork(t *testing.T, guest, softnet net.Conn) {
	t.Helper()
	sw, err := newNetwork(map[string]struct{}{}, &marker{})
	if err != nil {
		t.Fatalf("newNetwork: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = sw.Accept(ctx, softnet, "vfkit") }()
}

var guestMAC = net.HardwareAddr{0x02, 0x00, 0x00, 0x11, 0x22, 0x33}

func ethFrame(dst, src net.HardwareAddr, etherType uint16, payload []byte) []byte {
	f := make([]byte, 0, 14+len(payload))
	f = append(f, dst...)
	f = append(f, src...)
	f = append(f, byte(etherType>>8), byte(etherType))
	return append(f, payload...)
}

// TestARPRoundTrip proves the whole framing loop: a raw Ethernet ARP request
// off the socket is processed by the gvisor stack and answered with the
// gateway's MAC — via net.FileConn on a SOCK_DGRAM socketpair, the exact shape
// tart hands us.
func TestARPRoundTrip(t *testing.T) {
	guest, softnet := socketpairConns(t)
	startNetwork(t, guest, softnet)

	gwIP := net.ParseIP(gatewayIP).To4()
	guestIP := net.ParseIP("192.168.127.2").To4()
	bcast := net.HardwareAddr{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}

	// ARP request: who-has gatewayIP tell guestIP.
	arp := make([]byte, 28)
	binary.BigEndian.PutUint16(arp[0:], 1)      // htype ethernet
	binary.BigEndian.PutUint16(arp[2:], 0x0800) // ptype ipv4
	arp[4] = 6                                  // hlen
	arp[5] = 4                                  // plen
	binary.BigEndian.PutUint16(arp[6:], 1)      // op request
	copy(arp[8:14], guestMAC)
	copy(arp[14:18], guestIP)
	// arp[18:24] target MAC = 0
	copy(arp[24:28], gwIP)

	frame := ethFrame(bcast, guestMAC, 0x0806, arp)
	if _, err := guest.Write(frame); err != nil {
		t.Fatalf("write arp: %v", err)
	}

	_ = guest.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 1500)
	for {
		n, err := guest.Read(buf)
		if err != nil {
			t.Fatalf("read arp reply: %v", err)
		}
		if n < 42 {
			continue
		}
		etherType := binary.BigEndian.Uint16(buf[12:14])
		if etherType != 0x0806 {
			continue // ignore anything that isn't ARP
		}
		op := binary.BigEndian.Uint16(buf[20:22])
		if op != 2 {
			continue
		}
		senderMAC := net.HardwareAddr(buf[22:28])
		senderIP := net.IP(buf[28:32])
		if !senderIP.Equal(gwIP) {
			t.Fatalf("arp reply for wrong IP: %v", senderIP)
		}
		if senderMAC.String() != gatewayMAC {
			t.Fatalf("arp reply MAC = %s, want %s", senderMAC, gatewayMAC)
		}
		return // success
	}
}

// TestDHCPAssignsIP proves the embedded userspace DHCP server hands the guest an
// address in-subnet with the gateway as router/DNS — the "guest gets an IP via
// our DHCP" assertion, exercised without a VM.
func TestDHCPAssignsIP(t *testing.T) {
	guest, softnet := socketpairConns(t)
	startNetwork(t, guest, softnet)

	discover, err := dhcpv4.New(
		dhcpv4.WithHwAddr(guestMAC),
		dhcpv4.WithMessageType(dhcpv4.MessageTypeDiscover),
		dhcpv4.WithBroadcast(true),
	)
	if err != nil {
		t.Fatalf("build discover: %v", err)
	}
	sendDHCP(t, guest, discover)

	offer := recvDHCP(t, guest, dhcpv4.MessageTypeOffer)
	yip := offer.YourIPAddr
	if yip == nil || yip.IsUnspecified() {
		t.Fatalf("offer had no YourIPAddr")
	}
	_, subnet, _ := net.ParseCIDR(subnetCIDR)
	if !subnet.Contains(yip) {
		t.Fatalf("offered IP %v not in %s", yip, subnetCIDR)
	}
	router := offer.Router()
	if len(router) == 0 || !router[0].Equal(net.ParseIP(gatewayIP)) {
		t.Fatalf("offer router = %v, want %s", router, gatewayIP)
	}
	dnsSrv := offer.DNS()
	if len(dnsSrv) == 0 || !dnsSrv[0].Equal(net.ParseIP(gatewayIP)) {
		t.Fatalf("offer DNS = %v, want %s", dnsSrv, gatewayIP)
	}
	t.Logf("DHCP offered %v router=%v dns=%v", yip, router, dnsSrv)
}

func sendDHCP(t *testing.T, guest net.Conn, m *dhcpv4.DHCPv4) {
	t.Helper()
	payload := m.ToBytes()

	// UDP 68 -> 67
	udp := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint16(udp[0:], 68)
	binary.BigEndian.PutUint16(udp[2:], 67)
	binary.BigEndian.PutUint16(udp[4:], uint16(8+len(payload)))
	copy(udp[8:], payload)

	// IPv4 0.0.0.0 -> 255.255.255.255
	ip := make([]byte, 20)
	ip[0] = 0x45
	binary.BigEndian.PutUint16(ip[2:], uint16(20+len(udp)))
	ip[8] = 64 // TTL
	ip[9] = 17 // UDP
	copy(ip[12:16], net.IPv4zero.To4())
	copy(ip[16:20], net.IPv4bcast.To4())
	binary.BigEndian.PutUint16(ip[10:], ipChecksum(ip))
	packet := append(ip, udp...)

	bcast := net.HardwareAddr{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	frame := ethFrame(bcast, guestMAC, 0x0800, packet)
	if _, err := guest.Write(frame); err != nil {
		t.Fatalf("write dhcp: %v", err)
	}
}

func recvDHCP(t *testing.T, guest net.Conn, want dhcpv4.MessageType) *dhcpv4.DHCPv4 {
	t.Helper()
	_ = guest.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 2000)
	for {
		n, err := guest.Read(buf)
		if err != nil {
			t.Fatalf("read dhcp reply: %v", err)
		}
		if n < 14+20+8 {
			continue
		}
		if binary.BigEndian.Uint16(buf[12:14]) != 0x0800 {
			continue
		}
		ihl := int(buf[14]&0x0f) * 4
		if buf[14+9] != 17 { // not UDP
			continue
		}
		udpStart := 14 + ihl
		srcPort := binary.BigEndian.Uint16(buf[udpStart:])
		if srcPort != 67 {
			continue
		}
		payload := buf[udpStart+8 : n]
		m, err := dhcpv4.FromBytes(payload)
		if err != nil {
			continue
		}
		if m.MessageType() != want {
			continue
		}
		return m
	}
}

func ipChecksum(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(b[i:]))
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}
