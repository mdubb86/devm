// probe-stdin-fix: minimal experiment to prove the fix for the
// 2026-06-05 async-runtime-death.
//
// The base probe (probe-cold-start) established that:
//   * sbx 0.28.3 reaps the sandbox runtime if stdin EOFs during the
//     install phase, even though the `sbx run` process stays alive.
//   * Holding a pipe open as sbx's stdin keeps the runtime alive — but
//     ONLY while *someone* holds the pipe's write end. Once all writers
//     close, reader gets EOF and sbx reaps.
//
// This probe runs three variants of the same workflow:
//
//   "devnull"  : runCmd.Stdin = nil (current devm behavior)        → expect FAIL
//   "pipe"     : runCmd.Stdin = read-end of pipe; parent keeps the
//                write end open through the test                   → expect PASS (parent alive)
//   "pipe+fd3" : runCmd.Stdin = read-end of pipe; write end ALSO
//                passed via ExtraFiles (fd 3) so the child holds a
//                copy. We then close OUR copies of both ends.       → expect PASS (child holds)
//
// The "pipe+fd3" variant is the candidate for devm's actual fix:
// it doesn't tie sandbox lifetime to devm's lifetime. The pipe stays
// open as long as the child (sbx run) is alive, even after devm exits.
//
// Usage:
//
//	probe-stdin-fix devnull|pipe|pipe+fd3 <sandbox_name>
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

const kitYAML = `# probe-stdin-fix materialized kit
schemaVersion: "1"
kind: agent
name: probeshell
displayName: stdin fix probe
description: probe-stdin-fix materialized kit
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
    - command: 'apt-get update'
  startup:
    - command: ['sh', '-c', 'true']
      user: "1000"
      description: noop
`

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: probe-stdin-fix devnull|pipe|pipe+fd3 <sandbox_name>")
		os.Exit(2)
	}
	mode := os.Args[1]
	name := os.Args[2]

	logf := func(label, format string, args ...any) {
		fmt.Printf("[%s] %s %s\n",
			time.Now().Format("15:04:05.000"),
			label,
			fmt.Sprintf(format, args...))
	}

	ws, _ := os.MkdirTemp("", "probe-stdin-fix-ws-")
	kit, _ := os.MkdirTemp("", "probe-stdin-fix-kit-")
	defer os.RemoveAll(ws)
	defer os.RemoveAll(kit)
	if err := os.WriteFile(kit+"/spec.yaml", []byte(kitYAML), 0o644); err != nil {
		panic(err)
	}

	anchor := exec.Command("nohup", "sbx", "run", "--kit", kit, "--name", name,
		"probeshell", ws)
	anchor.Stdout = io.Discard
	anchor.Stderr = io.Discard

	var keepWrite *os.File // for "pipe" mode — we hold this in parent
	switch mode {
	case "devnull":
		anchor.Stdin = nil
	case "pipe":
		r, w, err := os.Pipe()
		if err != nil {
			panic(err)
		}
		anchor.Stdin = r
		keepWrite = w
		// We will NOT close keepWrite during the test. After Start,
		// close our copy of r (child has its own).
		defer r.Close()
	case "pipe+fd3":
		r, w, err := os.Pipe()
		if err != nil {
			panic(err)
		}
		anchor.Stdin = r
		anchor.ExtraFiles = []*os.File{w}
		// After Start, close BOTH our copies — child should keep the
		// pipe alive via its inherited fd 0 + fd 3.
		defer r.Close()
		defer w.Close()
	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q\n", mode)
		os.Exit(2)
	}

	logf("config", "mode=%s", mode)
	if err := anchor.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer func() {
		_ = exec.Command("sbx", "stop", name).Run()
		_ = exec.Command("sbx", "rm", "-f", name).Run()
		if anchor.Process != nil {
			_ = anchor.Process.Kill()
		}
		if keepWrite != nil {
			_ = keepWrite.Close()
		}
	}()
	logf("spawn", "pid=%d", anchor.Process.Pid)

	// For "pipe+fd3" mode, close our copies of the pipe ends NOW so we
	// can prove the child alone is keeping it open. (For "pipe" mode we
	// keep keepWrite open through the whole test.)
	if mode == "pipe+fd3" {
		// Close happens via the defers above when the anchor.Stdin and
		// ExtraFiles slice are released. To force-close NOW we close
		// the actual files:
		_ = anchor.Stdin.(*os.File).Close()
		_ = anchor.ExtraFiles[0].Close()
		logf("close", "parent copies of both pipe ends closed")
	}

	// Wait for sandbox to reach running.
	for time.Now().Add(60 * time.Second).After(time.Now()) {
		out, _ := exec.Command("sbx", "ls").Output()
		if matchesLine(string(out), name, "running") {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	logf("running", "")

	// Wait for exec-ready (fast — succeeds during install).
	for {
		if exec.Command("sbx", "exec", name, "true").Run() == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	logf("exec-ready", "")

	// THE TEST: idle for 20s. If sbx reaps the sandbox because of
	// stdin EOF, this is where it dies. Poll state every 2s.
	// Test: do sbx exec calls at a configurable interval. Find the
	// MAX interval at which the sandbox survives.
	interval := time.Second
	if os.Getenv("INTERVAL_MS") != "" {
		var ms int
		fmt.Sscanf(os.Getenv("INTERVAL_MS"), "%d", &ms)
		interval = time.Duration(ms) * time.Millisecond
	}
	totalTime := 30 * time.Second
	iterations := int(totalTime / interval)
	logf("idle-config", "interval=%s iterations=%d", interval, iterations)
	for i := 1; i <= iterations; i++ {
		time.Sleep(interval)
		state, exists := lsState(name)
		anchorAlive := isProcessAlive(anchor.Process.Pid)
		// Heartbeat: actively keep an sbx exec going if env is set.
		hb := ""
		if os.Getenv("HEARTBEAT") == "1" {
			err := exec.Command("sbx", "exec", name, "true").Run()
			hb = fmt.Sprintf(" hb_err=%v", err)
		}
		logf("idle", "t=%s state=%q exists=%v anchor=%v%s",
			time.Duration(i)*interval, state, exists, anchorAlive, hb)
		if !exists {
			logf("RESULT", "FAIL — sandbox died after %s idle (mode=%s)",
				time.Duration(i)*interval, mode)
			os.Exit(1)
		}
	}
	logf("RESULT", "PASS — sandbox survived %s idle (mode=%s)", totalTime, mode)
}

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

func matchesLine(out, name, status string) bool {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, name) && strings.Contains(line, status) {
			return true
		}
	}
	return false
}

func isProcessAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
