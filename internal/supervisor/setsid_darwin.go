//go:build darwin

package supervisor

import (
	"os/exec"
	"syscall"
)

// applySetsid pre-binds SysProcAttr.Setsid = true on the prepared
// exec.Cmd so the child detaches into its own process group and
// survives a SIGHUP delivered to the daemon at exit.
func applySetsid(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}
