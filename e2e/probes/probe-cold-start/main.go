// probe-cold-start: bisect the 2026-06-05 async-runtime-death race.
//
// Mimics devm RunShell's cold-start sequence step-by-step using a real
// Go binary with the SAME exec.Cmd shape devm uses (nohup + Stdin=nil
// + Stdout/Stderr to in-memory writers). Each step logs timing and
// re-checks `sbx ls` so we can pinpoint where the runtime vanishes
// relative to install-completion timing.
//
// The kit is materialized inline with a configurable slow install
// step. The pure-sbx test_sbx_05 fleet has already proven that EVERY
// individual ingredient (kit shape, nohup wrap, multi-step install,
// snapshot exec) works in isolation. This probe puts them together
// in Go (matching devm) AND varies install slowness, looking for the
// timing window that triggers the bug.
//
// Invocation:
//
//	probe-cold-start <sandbox_name> [--install-cmd CMD] [--skip-port-query] [--skip-snapshot] [--skip-user-shell]
//
// Exit code 0 = runtime healthy through every step. Non-zero = which
// step the runtime vanished at (1 = during/after IsRunning poll, 2 =
// during/after ExecReady, 3 = during/after port query, 4 = during/after
// snapshot, 5 = during/after user-shell spawn).
package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const kitYAML = `# probe-cold-start materialized kit
schemaVersion: "1"
kind: agent
name: probeshell
displayName: probe cold start
description: probe-cold-start materialized kit
agent:
  image: docker/sandbox-templates:shell
  aiFilename: CLAUDE.md
  persistence: persistent
  entrypoint:
    run: ["sh", "-c", "exec sleep infinity </dev/null"]
environment:
  variables:
    IS_SANDBOX: "1"
commands:
  install:
    - command: '%s'
    - command: 'touch /home/agent/.devm-install-done'
  startup:
    - command: ['sh', '-c', 'true']
      user: "1000"
      description: noop
`

func main() {
	installCmd := flag.String("install-cmd", "apt-get update", "install: command to use")
	skipPortQuery := flag.Bool("skip-port-query", false, "skip the sbx ports --json query (mimics devm port reconcile)")
	skipSnapshot := flag.Bool("skip-snapshot", false, "skip the snapshot exec (mimics devm WriteSnapshot)")
	skipUserShell := flag.Bool("skip-user-shell", false, "skip the user-shell spawn")
	preSnapSleep := flag.Duration("pre-snap-sleep", 0, "sleep this long after exec-ready before snapshot")
	heartbeat := flag.Bool("heartbeat", false, "during sleep, run `sbx exec NAME true` every second")
	flag.Parse()
	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: probe-cold-start <sandbox_name> [flags]")
		os.Exit(2)
	}
	name := flag.Arg(0)

	// Materialize kit + workspace.
	ws, err := os.MkdirTemp("", "probe-cold-start-ws-")
	must(err)
	kit, err := os.MkdirTemp("", "probe-cold-start-kit-")
	must(err)
	defer os.RemoveAll(ws)
	defer os.RemoveAll(kit)
	specPath := filepath.Join(kit, "spec.yaml")
	must(os.WriteFile(specPath, []byte(fmt.Sprintf(kitYAML, *installCmd)), 0o644))

	// In-memory writer to match devm's ring-buffer (closest io.Writer
	// substitute that's NOT a tty and NOT a file).
	var anchorOut writeBuf
	anchor := exec.Command("nohup", "sbx", "run", "--kit", kit, "--name", name,
		"probeshell", ws)
	stdinMode := os.Getenv("PROBE_ANCHOR_STDIN") // "devnull" (default) | "pipe-open"
	switch stdinMode {
	case "pipe-open":
		// Give sbx the read end of a pipe we hold open. No EOF until we
		// explicitly close — sbx's stdin stays open for the lifetime of
		// the probe process. Tests the "EOF on stdin = session ended"
		// hypothesis.
		r, w, err := os.Pipe()
		must(err)
		anchor.Stdin = r
		_ = w // hold the write end open for the lifetime of the probe
		defer w.Close()
		defer r.Close()
	default:
		anchor.Stdin = nil // /dev/null, immediate EOF
	}
	anchorStdioMode := os.Getenv("PROBE_ANCHOR_STDIO") // "memory" | "devnull" | "tty"
	switch anchorStdioMode {
	case "devnull", "":
		anchor.Stdout = nil // /dev/null
		anchor.Stderr = nil // /dev/null
	case "tty":
		anchor.Stdout = os.Stdout
		anchor.Stderr = os.Stderr
	default: // "memory"
		anchor.Stdout = &anchorOut
		anchor.Stderr = &anchorOut
	}
	logf := func(label, format string, args ...any) {
		fmt.Printf("[%s] %s %s\n",
			time.Now().Format("15:04:05.000"),
			label,
			fmt.Sprintf(format, args...))
	}
	logf("config", "stdio=%s install=%q", anchorStdioMode, *installCmd)
	must(anchor.Start())
	logf("spawn", "anchor pid=%d", anchor.Process.Pid)
	_ = anchorOut // silence unused when stdio≠memory

	exitCode := 0
	defer func() {
		_ = exec.Command("sbx", "stop", name).Run()
		_ = exec.Command("sbx", "rm", "-f", name).Run()
		if anchor.Process != nil {
			_ = anchor.Process.Kill()
		}
		if exitCode != 0 {
			fmt.Fprintf(os.Stderr, "\n--- anchor output ---\n%s\n---\n", anchorOut.String())
		}
		os.Exit(exitCode)
	}()

	// Step 1: poll for IsRunning. Same shape as devm's inline poll.
	step1Deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(step1Deadline) {
		out, _ := exec.Command("sbx", "ls").Output()
		if matchesLine(string(out), name, "running") {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	state, exists := lsState(name)
	logf("after-running-poll", "state=%q exists=%v", state, exists)
	if !exists || state != "running" {
		exitCode = 1
		return
	}

	// Step 2: WAIT FOR INSTALL MARKER, not just sbx exec true.
	useMarker := os.Getenv("PROBE_USE_MARKER") != "no"
	step2Deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(step2Deadline) {
		var ok bool
		if useMarker {
			ok = exec.Command("sbx", "exec", name, "test", "-f",
				"/home/agent/.devm-install-done").Run() == nil
		} else {
			ok = exec.Command("sbx", "exec", name, "true").Run() == nil
		}
		if ok {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	state, exists = lsState(name)
	logf("after-readiness", "marker=%v state=%q exists=%v", useMarker, state, exists)
	if !exists || state != "running" {
		exitCode = 2
		return
	}

	// Step 3: sbx ports --json (devm port reconcile's currentMappings).
	if !*skipPortQuery {
		t := time.Now()
		out, perr := exec.Command("sbx", "ports", name, "--json").CombinedOutput()
		logf("port-query", "dur=%s rc=%v out=%q",
			time.Since(t).Round(time.Millisecond), perr,
			strings.TrimSpace(string(out)))
		state, exists = lsState(name)
		logf("after-port-query", "state=%q exists=%v", state, exists)
		if !exists || state != "running" {
			exitCode = 3
			return
		}
	}

	// Step 4: snapshot exec (devm WriteSnapshot shape).
	if !*skipSnapshot {
		if *preSnapSleep > 0 {
			logf("pre-snap-sleep", "sleeping %s before snapshot", *preSnapSleep)
			// Poll sandbox state every second so we see the exact moment
			// it dies, plus whether the anchor process is still alive.
			tick := time.Now()
			deadline := tick.Add(*preSnapSleep)
			for time.Now().Before(deadline) {
				time.Sleep(1 * time.Second)
				state, exists := lsState(name)
				anchorAlive := isProcessAlive(anchor.Process.Pid)
				// Sample docker view directly — sbx state can lag.
				dockerOut, _ := exec.Command("docker", "ps", "-a",
					"--filter", "name="+name,
					"--format", "{{.Names}} status={{.Status}} state={{.State}}").Output()
				dockerInfo := strings.TrimSpace(string(dockerOut))
				if dockerInfo == "" {
					dockerInfo = "(no container)"
				}
				var hbResult string
				if *heartbeat {
					hbErr := exec.Command("sbx", "exec", name, "true").Run()
					hbResult = fmt.Sprintf(" hb_err=%v", hbErr)
				}
				logf("during-sleep", "t=%s sbx_state=%q exists=%v anchor=%v%s docker=[%s]",
					time.Since(tick).Round(time.Second), state, exists, anchorAlive, hbResult, dockerInfo)
				if !exists {
					break
				}
			}
			state, exists = lsState(name)
			logf("after-pre-snap-sleep", "state=%q exists=%v", state, exists)
		}
		t := time.Now()
		b64 := base64.StdEncoding.EncodeToString([]byte("# probe snapshot\n"))
		snapCmd := fmt.Sprintf(
			"mkdir -p /home/agent/.devm && echo %s | base64 -d > /home/agent/.devm/applied.yaml.tmp && mv /home/agent/.devm/applied.yaml.tmp /home/agent/.devm/applied.yaml",
			b64,
		)
		_, serr := exec.Command("sbx", "exec", name, "sh", "-c", snapCmd).CombinedOutput()
		logf("snapshot", "dur=%s rc=%v",
			time.Since(t).Round(time.Millisecond), serr)
		state, exists = lsState(name)
		logf("after-snapshot", "state=%q exists=%v", state, exists)
		if !exists || state != "running" {
			exitCode = 4
			return
		}
	}

	// Step 5: spawn user shell (sbx exec -it bash). Don't block — just
	// spawn and see if it starts.
	if !*skipUserShell {
		t := time.Now()
		us := exec.Command("sbx", "exec", "-it", name, "bash")
		us.Stdin = nil
		us.Stdout = io.Discard
		us.Stderr = io.Discard
		uerr := us.Start()
		logf("user-shell-spawn", "dur=%s start_err=%v",
			time.Since(t).Round(time.Millisecond), uerr)
		// Hold 2s to see if it stays alive
		time.Sleep(2 * time.Second)
		if us.Process != nil {
			_ = us.Process.Kill()
		}
		state, exists = lsState(name)
		logf("after-user-shell", "state=%q exists=%v", state, exists)
		if !exists || state != "running" {
			exitCode = 5
			return
		}
	}

	logf("end", "all steps complete; runtime healthy")
}

type writeBuf struct {
	b strings.Builder
}

func (w *writeBuf) Write(p []byte) (int, error) {
	return w.b.Write(p)
}
func (w *writeBuf) String() string { return w.b.String() }

func lsState(name string) (string, bool) {
	out, err := exec.Command("sbx", "ls").Output()
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == name {
			if len(fields) > 2 {
				return fields[2], true
			}
			return "", true
		}
	}
	return "", false
}

func isProcessAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Unix-only: sending signal 0 checks if the process exists.
	return proc.Signal(syscall.Signal(0)) == nil
}

func matchesLine(out, name, status string) bool {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, name) && strings.Contains(line, status) {
			return true
		}
	}
	return false
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}
