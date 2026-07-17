package softnet

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/containers/gvisor-tap-vsock/pkg/types"
)

type multiFlag []string

func (m *multiFlag) String() string     { return "" }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// Run is the softnet entrypoint (invoked via the `softnet` argv[0] alias). It
// parses tart's contract flags, assembles the netstack, egress, and DNS, then
// serves guest frames on the vm-fd connection until it closes or a signal
// arrives.
func Run(args []string) error {
	fs := flag.NewFlagSet("softnet", flag.ContinueOnError)
	vmFD := fs.Int("vm-fd", -1, "fd carrying the guest NIC socket")
	vmMac := fs.String("vm-mac-address", "", "guest NIC MAC")
	var allow, block, expose multiFlag
	fs.Var(&allow, "allow", "allow CIDR (recorded, ignored)")
	fs.Var(&block, "block", "block CIDR (recorded, ignored)")
	fs.Var(&expose, "expose", "expose spec (recorded, ignored)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse args: %w", err)
	}
	if *vmFD < 0 {
		return fmt.Errorf("--vm-fd is required")
	}
	_ = vmMac

	f := os.NewFile(uintptr(*vmFD), "vmnet")
	if f == nil {
		return fmt.Errorf("fd %d is not a valid file", *vmFD)
	}
	conn, err := net.FileConn(f)
	if err != nil {
		return fmt.Errorf("net.FileConn(fd %d): %w", *vmFD, err)
	}

	n, err := newNetwork()
	if err != nil {
		return fmt.Errorf("build netstack: %w", err)
	}
	e := newEgress(n)
	attachEgress(n, e)
	if err := n.startDNS(e); err != nil {
		return fmt.Errorf("dns: %w", err)
	}
	attachUDP(n, e)
	ing := newIngress(n)
	defer ing.close()
	if sock := os.Getenv("SOFTNET_CONTROL_SOCK"); sock != "" {
		closer, err := serveControl(sock, e, ing)
		if err != nil {
			return err
		}
		defer closer.Close()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := n.sw.Accept(ctx, conn, types.VfkitProtocol); err != nil {
		return fmt.Errorf("accept: %w", err)
	}
	return nil
}
