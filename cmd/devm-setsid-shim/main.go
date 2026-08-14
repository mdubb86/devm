// devm-setsid-shim wraps iron-proxy spawn so the child runs in a new
// session detached from the daemon's process tree, AND so iron-proxy's
// stdio survives the daemon dying.
//
// # Session detach
//
// The shim's own SysProcAttr can't Setsid (pexec makes the shim a
// process group leader before exec, and setsid(2) refuses process
// group leaders). Instead the shim spawns its argv[1:] as a CHILD
// with SysProcAttr.Setsid = true — Go's runtime runs Setsid post-fork
// pre-exec in the child, which is not a pgroup leader yet (its pgid
// is inherited from the shim). setsid() succeeds; the child becomes
// session leader in a new session.
//
// # Signal handling
//
// The shim IGNORES termination signals (SIGTERM/HUP/INT/QUIT) because
// launchctl bootout of the daemon walks the daemon's session and
// delivers SIGTERM to every descendant sharing that session —
// including this shim. If the shim forwarded that signal to
// iron-proxy, iron-proxy would die on every devm install/upgrade,
// defeating the whole reason for the setsid. Iron-proxy IS in its
// own session and won't receive the launchd SIGTERM directly; the
// shim's job is just to keep out of the way. If someone wants
// iron-proxy to stop, they signal iron-proxy's PID directly
// (supervisor.Stop does this via serviceapi.DiscoverIronProxies),
// NOT the shim.
//
// # Stdio isolation
//
// pexec captures the child's stdout/stderr via a pipe: write-end
// inherited by the shim (and passed to iron-proxy), read-end in a
// pexec goroutine inside the daemon. When the daemon dies, the
// read-end closes. Any subsequent write from iron-proxy raises EPIPE
// — and Go's runtime kills the process on SIGPIPE for fd 1/2. That's
// how a daemon restart reaches through the setsid boundary and kills
// iron-proxy despite everything above.
//
// The shim breaks the chain by interposing a fresh pipe between
// itself and iron-proxy: iron-proxy writes to a pipe the shim owns,
// and a shim goroutine tees those bytes to the inherited stdout. If
// the inherited stdout goes away (broken pipe), the shim absorbs the
// write error and keeps draining iron-proxy's pipe so iron-proxy
// never blocks and never sees EPIPE. Same treatment for stderr.
package main

import (
	"errors"
	"fmt"
	"io"
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
	//
	// SIGPIPE is in the ignore set too: Go's runtime terminates the
	// program on SIGPIPE for writes to fd 1 or fd 2. teeAbsorb writes
	// iron-proxy's output to os.Stdout / os.Stderr; when the daemon
	// dies, those writes raise SIGPIPE and would kill the shim, which
	// would then take iron-proxy down with it (child's pipe reader is
	// gone). Ignoring lets Write return EPIPE cleanly instead.
	signal.Ignore(syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGPIPE)

	// Fresh pipes between shim and child for stdout + stderr. The
	// child inherits our shim-owned write-ends; the shim reads and
	// tees to its own inherited stdout/stderr (which may or may not
	// be alive — see the stdio-isolation note in the package doc).
	outR, outW, err := os.Pipe()
	if err != nil {
		fatalf("stdout pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		fatalf("stderr pipe: %v", err)
	}

	cmd := exec.Command(os.Args[1], os.Args[2:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = outW
	cmd.Stderr = errW
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		fatalf("start: %v", err)
	}
	// Close our copies of the write-ends. Only the child holds a
	// write-end now, so when the child exits the reader loops below
	// see EOF and return cleanly.
	_ = outW.Close()
	_ = errW.Close()

	// Tee goroutines. On a healthy day, bytes flow shim → parent's
	// pipe (into pexec's LogWriter → per-project log file). On the
	// day the parent dies, the write to os.Stdout / os.Stderr fails
	// and we stop copying, but keep DRAINING the pipe from the
	// child so its writes complete and it never sees EPIPE.
	done := make(chan struct{}, 2)
	go func() { teeAbsorb(outR, os.Stdout); done <- struct{}{} }()
	go func() { teeAbsorb(errR, os.Stderr); done <- struct{}{} }()

	err = cmd.Wait()
	// Wait for reader goroutines to drain any remaining buffered
	// bytes after the child exits — otherwise the tail of iron-proxy's
	// output could be lost.
	<-done
	<-done

	if err == nil {
		os.Exit(0)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		os.Exit(exitErr.ExitCode())
	}
	fatalf("wait: %v", err)
}

// teeAbsorb copies src to dst until src EOFs. Once dst's write fails
// (typical cause: the daemon on the far end closed the pipe), it
// keeps draining src silently so writers into src never block and
// never see EPIPE. This is what protects iron-proxy from Go's default
// SIGPIPE-on-fd-1/2-terminates behavior across a daemon restart.
func teeAbsorb(src io.Reader, dst io.Writer) {
	buf := make([]byte, 32*1024)
	dstAlive := true
	for {
		n, err := src.Read(buf)
		if n > 0 && dstAlive {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				dstAlive = false
			}
		}
		if err != nil {
			return
		}
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "devm-setsid-shim: "+format+"\n", args...)
	os.Exit(1)
}
