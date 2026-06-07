// Command demo-shell-ux is a standalone runnable for iterating on the
// `devm shell` cold-start status UX without spawning a real sandbox.
//
// Usage:
//
//	go run ./cmd/demo-shell-ux              # realistic timings (~22s)
//	go run ./cmd/demo-shell-ux --fast       # 100ms per step
//	go run ./cmd/demo-shell-ux --warm       # warm-start path
//	go run ./cmd/demo-shell-ux --fail-step 3
//	go run ./cmd/demo-shell-ux --no-tty     # force PlainReporter
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
		fast     = flag.Bool("fast", false, "scale all delays to 100ms")
		warm     = flag.Bool("warm", false, "simulate the warm-start (shortcut) path")
		failStep = flag.Int("fail-step", 0, "user-step number to fail (counted from 1)")
		noTTY    = flag.Bool("no-tty", false, "force PlainReporter even on a TTY")
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

	// INSTANT spinner before we know cold vs warm.
	r.Start("starting up")
	time.Sleep(scale / 2)

	if *warm {
		// Warm path — single attach step, then ready.
		r.Step("attaching to running sandbox", false)
		time.Sleep(scale + scale/2)
		r.Step("ready", false)
		r.Stop()
		return
	}

	// Cold path. Three user-counted steps total: two from `install:`,
	// one from `services[*].startup:`. Bootstrap, init-volumes,
	// install-templates, ports, etc. are devm-internal — not counted.
	userTotal := 2 + 1
	r.SetTotal(userTotal)

	// Devm internals during bringup.
	r.Step("spawning sandbox", false)
	time.Sleep(scale / 2)

	r.Step("bootstrap", false)
	time.Sleep(12 * scale)

	// User install steps — these are counted.
	doStep(r, out, 1, "apt-get install -y jq", 1*scale+200*time.Millisecond, *failStep,
		"E: Unable to locate package jq")

	doStep(r, out, 2, "npm install -g typescript", 8*scale+400*time.Millisecond, *failStep,
		"npm ERR! code E404\nnpm ERR! 404 Not Found - GET https://registry.npmjs.org/typescript-nonexistent")

	r.Step("reconciling ports", false)
	time.Sleep(scale / 4)

	r.Step("init-volumes", false)
	time.Sleep(300 * time.Millisecond)

	r.Step("install service templates", false)
	time.Sleep(150 * time.Millisecond)

	// User startup step — counted.
	doStep(r, out, 3, "api startup", 500*time.Millisecond, *failStep,
		"Error: bind EADDRINUSE 0.0.0.0:8080")

	r.Step("ready", false)
	r.Stop()
}

// doStep runs a counted (user) step under the reporter. If failStep
// matches the user-step index, the step fails with a structured error
// block (mimicking formatFailureReport) and the program exits non-zero.
func doStep(r status.Reporter, out io.Writer, idx int, desc string, dur time.Duration, failStep int, captured string) {
	r.Step(desc, true)
	time.Sleep(dur)
	if failStep == idx {
		r.Fail()
		showFailureBlock(out, idx, desc, captured)
		r.Stop()
		os.Exit(1)
	}
}

// showFailureBlock emits a supervision-style failure block to mimic
// what formatFailureReport produces in the real path.
func showFailureBlock(out io.Writer, idx int, cmd, captured string) {
	fmt.Fprintln(out)
	fmt.Fprintf(out, "error: step %d failed (rc=1)\n", idx)
	fmt.Fprintf(out, "  command: %s\n", cmd)
	fmt.Fprintf(out, "  output (last %d bytes):\n", len(captured))
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
