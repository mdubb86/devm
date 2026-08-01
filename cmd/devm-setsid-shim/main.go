// devm-setsid-shim wraps iron-proxy spawn so the child runs in a new
// session detached from the daemon's process tree. Without this,
// `launchctl bootout` of the daemon (during `devm install`/upgrade)
// would kill iron-proxy too, dropping egress for all guest containers.
//
// The shim's own SysProcAttr can't Setsid (pexec makes the shim a
// process group leader before exec, and setsid(2) refuses process
// group leaders). Instead the shim spawns its argv[1:] as a CHILD
// with SysProcAttr.Setsid = true — Go's runtime runs Setsid post-fork
// pre-exec in the child, which is not a pgroup leader yet (its pgid
// is inherited from the shim). setsid() succeeds; the child becomes
// session leader in a new session.
//
// The shim then blocks on Wait() and does nothing else. It IGNORES
// termination signals (SIGTERM/HUP/INT/QUIT) because launchctl bootout
// of the daemon walks the daemon's session and delivers SIGTERM to
// every descendant sharing that session — including this shim. If the
// shim forwarded that signal to iron-proxy, iron-proxy would die on
// every devm install/upgrade, defeating the whole reason for the
// setsid: iron-proxy is meant to survive daemon restarts (see
// serviceapi.AdoptIronProxies). Iron-proxy IS in its own session and
// won't receive the launchd SIGTERM directly; the shim's job is just
// to keep out of the way. If someone wants iron-proxy to stop, they
// signal iron-proxy's PID directly (supervisor.Stop does this via
// serviceapi.DiscoverIronProxies), NOT the shim.
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

	// Ignore termination signals — see the package doc for the
	// launchctl-bootout rationale. signal.Ignore installs SIG_IGN
	// for the listed signals; Go's runtime never dispatches them
	// to any handler, and they don't kill the shim.
	//
	// Must be installed BEFORE cmd.Start(): a signal arriving between
	// Start and Ignore would hit Go's default disposition (kill the
	// shim on SIGTERM/HUP/INT/QUIT), leaving iron-proxy orphaned.
	signal.Ignore(syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT)

	cmd := exec.Command(os.Args[1], os.Args[2:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		fatalf("start: %v", err)
	}

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
