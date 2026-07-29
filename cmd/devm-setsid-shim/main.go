// devm-setsid-shim wraps iron-proxy spawn so the child runs in a new
// session detached from the daemon's process tree. Without this,
// `launchctl bootout` of the daemon (during `devm install`/upgrade)
// kills iron-proxy too, dropping egress for all guest containers.
//
// The shim's own SysProcAttr can't Setsid (pexec makes the shim a
// process group leader before exec, and setsid(2) refuses process
// group leaders). Instead the shim spawns its argv[1:] as a CHILD
// with SysProcAttr.Setsid = true — Go's runtime runs Setsid post-fork
// pre-exec in the child, which is not a pgroup leader yet (its pgid
// is inherited from the shim). setsid() succeeds; the child becomes
// session leader in a new session.
//
// The shim then blocks on Wait() and forwards signals so pexec's
// SIGTERM stop-flow reaches iron-proxy through the shim.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

func main() {
	if len(os.Args) < 2 {
		fatalf("usage: devm-setsid-shim <cmd> [args...]")
	}

	cmd := exec.Command(os.Args[1], os.Args[2:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	// Register signal handlers BEFORE Start(): a signal arriving between
	// Start() and Notify() would hit the shim's default Go disposition
	// (terminates on SIGTERM/INT/HUP/QUIT) instead of being forwarded —
	// leaving the child (already in a new session) orphaned without a
	// forwarded stop signal.
	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGQUIT)

	if err := cmd.Start(); err != nil {
		fatalf("start: %v", err)
	}

	// Forward pexec's stop signals so iron-proxy sees them.
	go func() {
		for sig := range sigCh {
			_ = cmd.Process.Signal(sig)
		}
	}()

	err := cmd.Wait()
	if err == nil {
		os.Exit(0)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		os.Exit(exitErr.ExitCode())
	}
	fatalf("wait: %v", err)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "devm-setsid-shim: "+format+"\n", args...)
	os.Exit(1)
}
