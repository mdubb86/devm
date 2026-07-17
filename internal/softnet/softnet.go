package softnet

import (
	"flag"
	"fmt"
	"os"
)

type multiFlag []string

func (m *multiFlag) String() string     { return "" }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// Run is the softnet entrypoint (invoked via the `softnet` argv[0] alias). It
// parses tart's contract flags, then (Task 7) assembles the netstack + control
// server and serves until the socket closes.
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
	_ = os.Getenv("SOFTNET_CONTROL_SOCK")
	// Assembly wired in Task 7.
	return fmt.Errorf("softnet: not yet assembled")
}
