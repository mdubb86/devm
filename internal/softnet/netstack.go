package softnet

import (
	"fmt"
	"net"

	"github.com/containers/gvisor-tap-vsock/pkg/services/dhcp"
	"github.com/containers/gvisor-tap-vsock/pkg/tap"
	"github.com/containers/gvisor-tap-vsock/pkg/types"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/network/arp"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/icmp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

// network holds the gvisor userspace netstack and its supporting switch/pool.
// DHCP and ARP are already serving by the time newNetwork returns; TCP
// forwarding, DNS, ingress expose, and UDP forwarding are attached by later
// tasks via methods on *network.
type network struct {
	sw     *tap.Switch
	stack  *stack.Stack
	ipPool *tap.IPPool
}

// newNetwork builds the link endpoint, switch, and gvisor stack, then starts
// DHCP. It mirrors virtualnetwork.New + addServices minus the TCP forwarder,
// DNS, ingress expose, and UDP forward wiring, which later tasks attach.
func newNetwork() (*network, error) {
	config := &types.Configuration{
		MTU:               MTU,
		Subnet:            SubnetCIDR,
		GatewayIP:         GatewayIP,
		GatewayMacAddress: GatewayMAC,
		GatewayVirtualIPs: []string{NATAliasIP},
		NAT:               map[string]string{NATAliasIP: HostLoopIP},
	}

	_, subnet, err := net.ParseCIDR(config.Subnet)
	if err != nil {
		return nil, fmt.Errorf("parse subnet: %w", err)
	}
	ipPool := tap.NewIPPool(subnet)
	ipPool.Reserve(net.ParseIP(config.GatewayIP), config.GatewayMacAddress)

	linkEP, err := tap.NewLinkEndpoint(false, MTU, config.GatewayMacAddress, config.GatewayIP, config.GatewayVirtualIPs)
	if err != nil {
		return nil, fmt.Errorf("link endpoint: %w", err)
	}
	sw := tap.NewSwitch(false)
	linkEP.Connect(sw)
	sw.Connect(linkEP)

	s, err := buildStack(config, linkEP)
	if err != nil {
		return nil, err
	}

	if err := startDHCP(config, s, ipPool); err != nil {
		return nil, fmt.Errorf("dhcp: %w", err)
	}

	return &network{sw: sw, stack: s, ipPool: ipPool}, nil
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

func startDHCP(config *types.Configuration, s *stack.Stack, ipPool *tap.IPPool) error {
	server, err := dhcp.New(config, s, ipPool)
	if err != nil {
		return err
	}
	go func() { _ = server.Serve() }()
	return nil
}
