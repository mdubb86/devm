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

// Lifecycle
//
// softnet is a child `tart run --net-softnet` forks internally (see the
// `serves guest frames on the vm-fd connection` note on Run below). Its
// lifetime tracks the vm-fd, not the daemon and not the guest kernel.
// There are three distinct "restart" cases; softnet handles them
// differently:
//
//  1. VM-down (`tart run` process exits). The vm-fd closes →
//     acceptUntilShutdown's read loop ends → softnet exits. A fresh
//     `tart run` forks a brand-new softnet; no state carries over.
//     Historically this didn't always fire cleanly (see
//     internal/serviceapi/softnet_reap.go: ReapOrphanSoftnets), so PPID==1
//     detection catches survivors on daemon startup.
//
//  2. In-place guest reboot (`tart run` persists, guest kernel reboots).
//     The vm-fd stays open, so softnet keeps running and its gvisor
//     netstack — built once by newNetwork(), never rebuilt — is REUSED
//     across the guest boot. This works implicitly today because:
//     - Tart's guest MAC is stable across reboots, so the guest's
//     fresh DHCPDISCOVER lands the same lease from ipPool.
//     - gvisor's TCP conntrack ages out stale flows on its own timeout;
//     guest-side is dead, so real traffic RSTs correctly.
//     - ARP cache (guest MAC → guest IP) is still valid.
//     - Everything Mac-side (ingress listeners, egress policy) is
//     orthogonal to guest state.
//     Nothing explicitly detects the reboot or resets softnet's state;
//     if a wedge surfaces (e.g. long hangs to the guest immediately post-
//     reboot), the workaround is `devm stop && devm start` and the fix
//     would be to detect a fresh DHCPDISCOVER against an already-leased
//     MAC and rebuild the gvisor stack. Not built.
//
//  3. Daemon restart (VM stays up, daemon reconnects). softnet is REUSED:
//     internal/serviceapi/softnet_control.go's discoverSoftnet reconnects
//     to the existing control socket rather than respawning softnet,
//     because the daemon never owned the process in the first place.
//     Same reasoning as case 2 — the netstack persists because it's fine
//     for it to persist.
//
// Run is the softnet entrypoint (invoked via the `softnet` argv[0] alias). It
// parses tart's contract flags, assembles the netstack, egress, and DNS, then
// serves guest frames on the vm-fd connection until it closes or a signal
// arrives. cfg is the daemon's compiled-in identity (prod vs. e2e) — it
// selects which helper socket the low-port ingress branch dials, so an
// e2e daemon's softnet binds through the e2e helper, not prod's.
func Run(cfg identity.Config, args []string) error {
	fs := flag.NewFlagSet("softnet", flag.ContinueOnError)
	vmFD := fs.Int("vm-fd", -1, "fd carrying the guest NIC socket")
	_ = fs.String("vm-mac-address", "", "guest NIC MAC (accepted from tart; ignored — softnet learns the MAC via ARP/DHCP)")
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
