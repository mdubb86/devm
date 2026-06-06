// probe-publish-triggered — a deliberately bug-shaped variant of
// e2e/probes/probe-publish/main.go that REPRODUCES Quirk #6.
//
// Two structural choices distinguish it from the clean probe:
//
//  1. The anchor (`nohup sbx run …`) is spawned through a Spawner
//     interface that wraps `*exec.Cmd` in `&execCmd{c: c}` and
//     returns it as a `SpawnedCmd` interface — mirroring the
//     production `ExecSpawner.Start` / `SpawnedCmd` shape that
//     used to be in `internal/orchestrator/exec.go`.
//
//  2. The "wait for running" loop uses `time.NewTicker(250ms)` +
//     `select { ctx.Done / runDone / ticker.C }`, mirroring the
//     `waitForRunning` helper that used to be called from
//     `RunShell`.
//
// Either pattern alone is insufficient to reproduce the phantom
// (in the strip-devm-publish bisection, inline-waitFor with
// ExecSpawner-kept scored 8/9 publish-OK on test_07). The pair
// together is the trigger — bisected to 10/10 baseline → 8/9
// with one fix → 10/10 with both fixed.
//
// The clean probe (e2e/probes/probe-publish/) is pinned by
// test_sbx_interop_03_anchor_publish_baseline.py. This triggered probe is
// pinned by test_sbx_interop_04_anchor_spawner_ticker.py. Under sbx 0.31+
// the publish phantom is fixed, so both stay green. If sbx ever
// regresses, the asymmetry (baseline green / triggered red)
// pins the cause to the upstream — preventing a future refactor
// of internal/orchestrator/shell.go from quietly reintroducing
// either pattern around the anchor spawn without
// the regression test catching it (via test_07 going red and
// the trigger test going green).
//
// Invocation, exit codes, and flow shape match the clean probe;
// see that file for prose. Only the two structural pieces above
// differ.
package main

import (
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

func encodeBase64(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

const (
	hostPort    = 50261 // distinct from probe-publish's 50260 so they can run concurrently
	sandboxPort = 8080
	stableNeed  = 8.0
	observeFor  = 12.0
	pollInt     = 250 * time.Millisecond
)

// --- Trigger piece #1: Spawner / SpawnedCmd interface around the anchor.
// Mirrors internal/orchestrator/exec.go shape before the Quirk #6 fix.

type spawner interface {
	Start(name string, args ...string) (spawnedCmd, error)
}
type spawnedCmd interface {
	Wait() (int, error)
	Kill() error
	Pid() int
}
type execSpawner struct{}

func (s *execSpawner) Start(name string, args ...string) (spawnedCmd, error) {
	c := exec.Command(name, args...)
	c.Stdin = nil
	c.Stdout = nil
	c.Stderr = os.Stderr
	if err := c.Start(); err != nil {
		return nil, err
	}
	return &execCmdWrap{c: c}, nil
}

type execCmdWrap struct{ c *exec.Cmd }

func (e *execCmdWrap) Wait() (int, error) {
	err := e.c.Wait()
	if err == nil {
		return 0, nil
	}
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode(), nil
	}
	return -1, err
}
func (e *execCmdWrap) Kill() error {
	if e.c.Process == nil {
		return nil
	}
	return e.c.Process.Kill()
}
func (e *execCmdWrap) Pid() int {
	if e.c.Process == nil {
		return 0
	}
	return e.c.Process.Pid
}

// --- Trigger piece #2: waitForRunning helper using time.NewTicker +
// select with a runDone channel. Mirrors internal/orchestrator/
// shell.go's waitForRunning before the Quirk #6 fix.

func waitForRunning(ctx context.Context, name string, runDone <-chan error, timeout, poll time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		if running(name) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-runDone:
			if err != nil {
				return fmt.Errorf("anchor exited before running: %w", err)
			}
			return fmt.Errorf("anchor exited before running")
		case <-ticker.C:
			if time.Now().After(deadline) {
				return fmt.Errorf("sandbox %s never reached running within %s", name, timeout)
			}
		}
	}
}

func running(name string) bool {
	out, _ := exec.Command("sbx", "ls").Output()
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, name) && strings.Contains(line, "running") {
			return true
		}
	}
	return false
}

func main() {
	useNohup := flag.Bool("nohup", false, "wrap anchor in `nohup` (devm production shape)")
	flag.Parse()
	if flag.NArg() < 3 {
		fmt.Fprintln(os.Stderr, "usage: probe-publish-triggered [--nohup] <kit_dir> <workspace> <sandbox_name>")
		os.Exit(2)
	}
	kitDir, workspace, name := flag.Arg(0), flag.Arg(1), flag.Arg(2)
	spec := fmt.Sprintf("%d:%d", hostPort, sandboxPort)

	fmt.Printf("name=%s nohup=%v\n", name, *useNohup)

	if err := os.Chdir(workspace); err != nil {
		fmt.Fprintf(os.Stderr, "chdir %s: %v\n", workspace, err)
		os.Exit(2)
	}
	fmt.Printf("cwd=%s\n", workspace)

	// 1. Spawn anchor through the Spawner interface — TRIGGER PIECE #1.
	var sp spawner = &execSpawner{}
	sbxArgs := []string{"run", "--kit", kitDir, "--name", name, "anchortest", workspace}
	var cmdName string
	var cmdArgs []string
	if *useNohup {
		cmdName = "nohup"
		cmdArgs = append([]string{"sbx"}, sbxArgs...)
	} else {
		cmdName = "sbx"
		cmdArgs = sbxArgs
	}
	anchor, err := sp.Start(cmdName, cmdArgs...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "spawn anchor: %v\n", err)
		os.Exit(2)
	}
	defer func() {
		_ = exec.Command("sbx", "stop", name).Run()
		_ = exec.Command("sbx", "rm", name).Run()
		_ = anchor.Kill()
	}()

	runDone := make(chan error, 1)
	go func() {
		_, werr := anchor.Wait()
		runDone <- werr
	}()

	// 2. waitForRunning via the ticker+select helper — TRIGGER PIECE #2.
	ctx := context.Background()
	if err := waitForRunning(ctx, name, runDone, 60*time.Second, pollInt); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(2)
	}
	fmt.Printf("running   @ %s\n", time.Now().Format("15:04:05.000"))

	// 3. Wait exec-ready (using clean inline loop — the bug is in the
	// anchor spawn + wait-for-running pair, not here).
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if exec.Command("sbx", "exec", name, "true").Run() == nil {
			break
		}
		time.Sleep(pollInt)
	}
	fmt.Printf("exec-rdy  @ %s\n", time.Now().Format("15:04:05.000"))

	// 4. Publish (retry on no-container-endpoint, like the clean probe).
	pubDeadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(pubDeadline) {
		t := time.Now()
		out, err := exec.Command("sbx", "ports", name, "--publish", spec).CombinedOutput()
		fmt.Printf("publish   @ %s dur=%s rc=%v out=%q\n",
			time.Now().Format("15:04:05.000"),
			time.Since(t).Round(time.Millisecond), err, strings.TrimSpace(string(out)))
		if err == nil {
			break
		}
		if strings.Contains(string(out), "no container endpoint") {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		fmt.Fprintf(os.Stderr, "unexpected publish error: %s\n", out)
		os.Exit(2)
	}

	// 4b. Snapshot — match clean probe.
	const snapContent = "# devm-applied-config\nschemaVersion: \"1\"\n"
	const snapPath = "/home/agent/.devm/applied.yaml"
	b64 := encodeBase64(snapContent)
	snapCmd := fmt.Sprintf("mkdir -p $(dirname %s) && echo %s | base64 -d > %s.tmp && mv %s.tmp %s",
		snapPath, b64, snapPath, snapPath, snapPath)
	_ = exec.Command("sbx", "exec", name, "sh", "-c", snapCmd).Run()

	// 4c. Spawn user shell — match clean probe.
	userShell := exec.Command("sbx", "exec", "-it", name, "bash")
	userShell.Stdin = os.Stdin
	userShell.Stdout = os.Stdout
	userShell.Stderr = os.Stderr
	_ = userShell.Start()
	defer func() {
		if userShell.Process != nil {
			_ = userShell.Process.Kill()
		}
	}()

	// 5. Observe — same as clean probe.
	t0 := time.Now()
	visibleSeconds := 0.0
	prevPresent := false
	lastT := t0
	for time.Since(t0).Seconds() < observeFor {
		present := portVisible(name)
		now := time.Now()
		if prevPresent {
			visibleSeconds += now.Sub(lastT).Seconds()
		}
		if present != prevPresent {
			fmt.Printf("  +%5.2fs  %s\n", time.Since(t0).Seconds(),
				yn(present, "VISIBLE", "GONE"))
		}
		prevPresent = present
		lastT = now
		time.Sleep(500 * time.Millisecond)
	}
	if prevPresent {
		visibleSeconds += time.Since(lastT).Seconds()
	}
	fmt.Printf("\ntotal_visible_seconds=%.2f (need %.2f for PASS)\n",
		visibleSeconds, stableNeed)
	if visibleSeconds >= stableNeed {
		fmt.Println("RESULT: PASS — mapping stable (Quirk #6 trigger did NOT fire this run)")
		os.Exit(0)
	}
	fmt.Println("RESULT: FAIL — mapping vanished (Quirk #6 trigger fired)")
	os.Exit(1)
}

func portVisible(name string) bool {
	out, _ := exec.Command("sbx", "ports", name, "--json").Output()
	return strings.Contains(string(out), fmt.Sprintf("\"host_port\": %d", hostPort))
}

func yn(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}
