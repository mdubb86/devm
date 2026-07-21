package softnet

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/containers/gvisor-tap-vsock/pkg/tap"
	"github.com/containers/gvisor-tap-vsock/pkg/types"

	"github.com/mdubb86/devm/internal/identity"
)

type multiFlag []string

func (m *multiFlag) String() string     { return "" }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// Run is the softnet entrypoint (invoked via the `softnet` argv[0] alias). It
// parses tart's contract flags, assembles the netstack, egress, and DNS, then
// serves guest frames on the vm-fd connection until it closes or a signal
// arrives. cfg is the daemon's compiled-in identity (prod vs. e2e) — it
// selects which helper socket the low-port ingress branch dials, so an
// e2e daemon's softnet binds through the e2e helper, not prod's.
func Run(cfg identity.Config, args []string) error {
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
	ing := newIngress(cfg, n)
	defer ing.close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if sock := os.Getenv("SOFTNET_CONTROL_SOCK"); sock != "" {
		// stop is also the "shutdown" control op's trigger (see
		// applyControl in control.go): the daemon's /vm/stop sends that
		// message over this socket because softnet is a child `tart run
		// --net-softnet` forks internally — invisible to the daemon's
		// process supervisor and therefore never reachable by a signal
		// the supervisor sends directly.
		closer, err := serveControl(sock, e, ing, stop)
		if err != nil {
			return err
		}
		defer closer.Close()
	}

	return acceptUntilShutdown(ctx, n.sw, conn)
}

// acceptUntilShutdown runs sw.Accept on conn until it returns or ctx is
// cancelled (by a SIGTERM/SIGINT or the "shutdown" control op — see Run).
//
// sw.Accept's read loop only checks ctx.Done() between reads
// (gvisor-tap-vsock's pkg/tap.Switch.rxNonStream) — it does not select on
// ctx while blocked inside conn.Read. Once the guest has gone quiet (e.g.
// right after `systemctl poweroff`, with no more frames to read),
// cancelling ctx alone leaves that Read parked forever and the process
// never exits — the orphan-softnet bug this closes. Closing conn directly
// forces the blocked Read to return an error immediately, so a shutdown
// request always unblocks Accept promptly instead of only when the next
// packet happens to arrive.
func acceptUntilShutdown(ctx context.Context, sw *tap.Switch, conn net.Conn) error {
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	if err := sw.Accept(ctx, conn, types.VfkitProtocol); err != nil {
		if ctx.Err() != nil {
			// Expected: shutdown closed conn out from under a blocked
			// Read, above. Not a real failure.
			return nil
		}
		return fmt.Errorf("accept: %w", err)
	}
	return nil
}
