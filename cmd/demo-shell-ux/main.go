// Command demo-shell-ux is a standalone runnable for iterating on the
// `devm shell` cold-start status UX without spawning a real sandbox.
//
// It drives the same status.Reporter interface that the real RunShell
// uses, against a scripted scenario with realistic delays.
//
// Usage:
//
//	go run ./cmd/demo-shell-ux                 # realistic timings (~20s)
//	go run ./cmd/demo-shell-ux --fast          # 100ms per step
//	go run ./cmd/demo-shell-ux --warm          # warm-start path (shortcut)
//	go run ./cmd/demo-shell-ux --fail install 3  # simulate failure
//	go run ./cmd/demo-shell-ux --no-tty        # force PlainReporter
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/mtwaage/devm/internal/status"
)

func main() {
	var (
		fast      = flag.Bool("fast", false, "scale all delays to 100ms")
		warm      = flag.Bool("warm", false, "simulate the warm-start (shortcut) path")
		failPhase = flag.String("fail-phase", "", `simulate failure: "install" or "startup"`)
		failStep  = flag.Int("fail-step", 0, "step number to fail")
		noTTY     = flag.Bool("no-tty", false, "force PlainReporter even on a TTY")
	)
	flag.Parse()

	var out io.Writer = os.Stderr
	var r status.Reporter
	if *noTTY {
		r = &status.PlainReporter{Out: out}
	} else {
		r = status.New(out)
	}

	scale := time.Second
	if *fast {
		scale = 100 * time.Millisecond
	}

	// INSTANT spinner — show liveness before we know anything else.
	r.Start("starting up")

	// Brief "are we cold or warm" beat (devm checks IsRunning).
	time.Sleep(scale / 2)

	if *warm {
		// Warm path: just attaching to the running sandbox.
		r.StepStart("", 0, "attaching to running sandbox")
		time.Sleep(scale + scale/2)
		r.StepDone("", 0, scale+scale/2)
		r.StepStart("", 0, "ready")
		r.StepDone("", 0, 0)
		r.Stop()
		return
	}

	// Cold path: each transition is its own labeled step.
	r.StepStart("", 0, "spawning sandbox")
	time.Sleep(scale / 2)
	r.StepDone("", 0, scale/2)

	// Phase: install (bootstrap.sh + 2 user steps + sentinel — display 3 steps)
	r.PhaseStart("install", 3)

	doStep(r, out, "install", 1, "bootstrap.sh",
		12*scale, *failPhase, *failStep,
		`E: dpkg was interrupted, you must manually run 'dpkg --configure -a'`)

	doStep(r, out, "install", 2, "apt-get install -y jq",
		1*scale+200*time.Millisecond, *failPhase, *failStep,
		`E: Unable to locate package jq`)

	doStep(r, out, "install", 3, "npm install -g typescript",
		8*scale+400*time.Millisecond, *failPhase, *failStep,
		"npm ERR! code E404\nnpm ERR! 404 Not Found - GET https://registry.npmjs.org/typescript-nonexistent")

	r.PhaseDone("install", 21*scale+600*time.Millisecond)

	r.StepStart("", 0, "reconciling ports")
	time.Sleep(scale / 4)
	r.StepDone("", 0, scale/4)

	// Phase: startup
	r.PhaseStart("startup", 2)

	doStep(r, out, "startup", 1, "init-volumes",
		300*time.Millisecond, *failPhase, *failStep,
		"chown: cannot access '/workspace/.cache': Permission denied")

	doStep(r, out, "startup", 2, "api startup",
		500*time.Millisecond, *failPhase, *failStep,
		"Error: bind EADDRINUSE 0.0.0.0:8080")

	r.PhaseDone("startup", 800*time.Millisecond)

	r.StepStart("", 0, "ready")
	r.StepDone("", 0, 22*scale+800*time.Millisecond)
	r.Stop()
}

// doStep runs a step under the reporter. If failPhase/failStep matches,
// the step ends with StepFail + a structured error block and the
// program exits non-zero. Otherwise StepDone.
func doStep(r status.Reporter, out io.Writer, phase string, n int, desc string,
	dur time.Duration, failPhase string, failStep int, captured string,
) {
	r.StepStart(phase, n, desc)
	time.Sleep(dur)
	if failPhase == phase && failStep == n {
		r.StepFail(phase, n, dur)
		showFailureBlock(out, phase, n, desc, captured)
		r.Stop()
		os.Exit(1)
	}
	r.StepDone(phase, n, dur)
}

// showFailureBlock emits the supervision-style failure block to mimic
// what formatFailureReport produces in the real path. Lets us see the
// transition from ✗ status line into the structured error.
func showFailureBlock(out io.Writer, phase string, n int, cmd, captured string) {
	// First the step-fail line (the reporter does this — we call StepFail).
	// Then the captured block, mirroring formatFailureReport.
	fmt.Fprintln(out)
	fmt.Fprintf(out, "error: %s step %d failed (rc=1)\n", phase, n)
	fmt.Fprintf(out, "  command: %s\n", cmd)
	fmt.Fprintf(out, "  output (last %d bytes of /tmp/.devm-%s/%s-%d/current):\n", len(captured), phase, phase, n)
	for _, line := range splitLines(captured) {
		fmt.Fprintf(out, "    %s\n", line)
	}
}

func splitLines(s string) []string {
	out := []string{}
	cur := ""
	for _, c := range s {
		if c == '\n' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(c)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func formatElapsed(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	m := int(d / time.Minute)
	s := int((d % time.Minute) / time.Second)
	return fmt.Sprintf("%dm%02ds", m, s)
}
