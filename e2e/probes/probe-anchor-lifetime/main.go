// probe-anchor-lifetime — pin devm's anchor-spawn primitive.
//
// Mirrors internal/orchestrator/shell.go's production shape: spawn
// bare `sbx run ...` (no nohup) under pty.StartWithSize, wait for the
// sandbox to reach running, CLOSE THE PTY MASTER, then assert the
// anchor process is still alive AND sbx ls still shows the sandbox
// running.
//
// Why this pins the interop primitive: devm spawns the anchor and
// then continues its orchestration — port reconcile, user-shell
// spawn, eventually exiting. The master will close at various points
// (devm exit at minimum). If closing the master killed the anchor,
// devm's whole anchor-alive model would collapse.
//
// Empirically (this probe, sbx 0.31): the anchor survives master
// close because sbx ignores SIGHUP when running under a controlling
// TTY (TUI-style signal handling — same as vim/less/tmux). This
// replaced the historical nohup wrap, simplified out 2026-06-06.
//
// Exit codes:
//
//	0 — anchor PID still alive AND sbx ls still shows running 3s
//	    after master close. Interop pin holds.
//	1 — anchor PID dead after master close. The primitive broke
//	    (sbx changed signal handling, or PTY config drifted).
//	2 — anchor alive but sbx ls no longer shows running. Surprise
//	    state — neither expected outcome.
//	3 — sandbox never reached running, or kit/workspace setup
//	    failed. Probe couldn't run.
//	4 — usage error.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/creack/pty"
)

const kitYAML = `schemaVersion: '1'
kind: agent
name: probe
displayName: anchor lifetime probe
description: anchor lifetime probe
agent:
  image: docker/sandbox-templates:shell
  aiFilename: CLAUDE.md
  entrypoint:
    run:
    - sh
    - -c
    - exec sleep infinity </dev/null
environment:
  variables:
    IS_SANDBOX: '1'
commands:
  install:
  - command: 'true'
  startup:
  - command: ["sh", "-c", "true"]
    user: '1000'
    description: noop
`

const (
	exitOK          = 0
	exitAnchorDead  = 1
	exitSbxLost     = 2
	exitBringupFail = 3
	exitUsage       = 4
)

func main() {
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: probe-anchor-lifetime <sandbox_name>")
		os.Exit(exitUsage)
	}
	sandboxName := flag.Arg(0)

	kitDir, err := os.MkdirTemp("", "anchor-lifetime-kit-")
	if err != nil {
		fail("mkdir kit: %v", err)
	}
	defer os.RemoveAll(kitDir)
	if err := os.WriteFile(filepath.Join(kitDir, "spec.yaml"), []byte(kitYAML), 0644); err != nil {
		fail("write spec: %v", err)
	}
	ws, err := os.MkdirTemp("", "anchor-lifetime-ws-")
	if err != nil {
		fail("mkdir ws: %v", err)
	}
	defer os.RemoveAll(ws)

	defer func() {
		_ = exec.Command("sbx", "rm", "-f", sandboxName).Run()
	}()

	// Production shape: bare sbx run under PTY. No nohup wrapping.
	cmd := exec.Command("sbx", "run", "--kit", kitDir, "--name", sandboxName, "probe", ws)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		fail("pty.StartWithSize: %v", err)
	}

	// Drain master in background. Don't wait for drain to finish on
	// close — Go's runtime can leave a blocked Read on a tty fd
	// unwakeable when the fd is closed from another goroutine.
	go func() {
		_, _ = io.Copy(io.Discard, ptmx)
	}()

	if !waitRunning(sandboxName, 60*time.Second) {
		fmt.Fprintln(os.Stderr, "BRINGUP_FAIL: sandbox never reached running")
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		os.Exit(exitBringupFail)
	}
	fmt.Println("PHASE: running")

	// THE PIN: close the PTY master. Kernel SHOULD send SIGHUP to
	// processes whose controlling tty is the slave (= the anchor).
	// sbx's TUI-style signal handling absorbs it.
	if err := ptmx.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: ptmx.Close: %v\n", err)
	}
	fmt.Println("PHASE: master_closed")

	time.Sleep(3 * time.Second)

	anchorAlive := isAlive(cmd.Process.Pid)
	fmt.Printf("PHASE: anchor_alive=%v pid=%d\n", anchorAlive, cmd.Process.Pid)
	if !anchorAlive {
		_ = cmd.Wait()
		os.Exit(exitAnchorDead)
	}

	sbxOK := isRunning(sandboxName)
	fmt.Printf("PHASE: sbx_running=%v\n", sbxOK)
	_ = cmd.Process.Kill()
	_ = cmd.Wait()

	if !sbxOK {
		os.Exit(exitSbxLost)
	}
	os.Exit(exitOK)
}

func waitRunning(name string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if isRunning(name) {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

func isRunning(name string) bool {
	out, err := exec.Command("sbx", "ls", "--json").Output()
	if err != nil {
		return false
	}
	var resp struct {
		Sandboxes []map[string]any `json:"sandboxes"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return false
	}
	for _, sb := range resp.Sandboxes {
		if n, _ := sb["name"].(string); n != name {
			continue
		}
		if state, _ := sb["status"].(string); state == "running" {
			return true
		}
	}
	return false
}

func isAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "BRINGUP_FAIL: "+format+"\n", args...)
	os.Exit(exitBringupFail)
}
