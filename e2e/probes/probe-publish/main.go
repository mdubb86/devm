// Minimal Go probe that does the bare-minimum publish flow against a
// real sbx sandbox, with no devm orchestration around it.
//
// Purpose: bisect the test_07 publish-phantom failure. We've confirmed
// pure-sbx (Python subprocess) can publish reliably. devm's full
// orchestrator cannot. This probe lives in the middle: a Go binary
// using exec.Command, but without devm's lock, render, runDone
// goroutine, ReconcilePortsWithRunner, snapshot, or user-shell spawn.
//
// Invocation:
//
//	probe-publish [--nohup] <kit_dir> <workspace> <sandbox_name>
//
// Flow:
//  1. Spawn anchor: (nohup?) sbx run --kit <kit> --name <name> anchortest <ws>
//  2. Wait until `sbx ls` shows running
//  3. Wait until `sbx exec NAME true` succeeds
//  4. sbx ports NAME --publish 50260:8080
//  5. Poll sbx ports NAME --json every 500ms for 10s
//  6. Report timeline + final pass/fail (mapping must be visible for
//     >=5 of the 10 seconds)
//
// Exit code:
//
//	0 — mapping was visible for >=5s
//	1 — mapping was NOT visible for >=5s (phantom-like failure)
//	2 — bring-up failure
package main

import (
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
	hostPort    = 50260
	sandboxPort = 8080
	stableNeed  = 8.0  // seconds visible required to declare success
	observeFor  = 12.0 // observation window after publish
)

func main() {
	useNohup := flag.Bool("nohup", false, "wrap anchor in `nohup` (devm production shape)")
	flag.Parse()
	if flag.NArg() < 3 {
		fmt.Fprintln(os.Stderr, "usage: probe-publish [--nohup] <kit_dir> <workspace> <sandbox_name>")
		os.Exit(2)
	}
	kitDir, workspace, name := flag.Arg(0), flag.Arg(1), flag.Arg(2)
	spec := fmt.Sprintf("%d:%d", hostPort, sandboxPort)

	fmt.Printf("name=%s nohup=%v\n", name, *useNohup)

	// 1. Spawn anchor.
	var anchor *exec.Cmd
	if *useNohup {
		anchor = exec.Command("nohup", "sbx", "run", "--kit", kitDir,
			"--name", name, "anchortest", workspace)
	} else {
		anchor = exec.Command("sbx", "run", "--kit", kitDir,
			"--name", name, "anchortest", workspace)
	}
	anchor.Stdin = nil
	anchor.Stdout = nil
	anchor.Stderr = nil
	if err := anchor.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "spawn anchor: %v\n", err)
		os.Exit(2)
	}
	defer func() {
		_ = exec.Command("sbx", "stop", name).Run()
		_ = exec.Command("sbx", "rm", name).Run()
		if anchor.Process != nil {
			_ = anchor.Process.Kill()
		}
	}()

	// 2. Wait running.
	if !waitFor("running", 60*time.Second, func() bool {
		out, _ := exec.Command("sbx", "ls").Output()
		return matchesLine(string(out), name, "running")
	}) {
		fmt.Fprintf(os.Stderr, "sandbox never reached running\n")
		os.Exit(2)
	}
	fmt.Printf("running   @ %s\n", time.Now().Format("15:04:05.000"))

	// 3. Wait exec-ready.
	if !waitFor("exec-ready", 30*time.Second, func() bool {
		return exec.Command("sbx", "exec", name, "true").Run() == nil
	}) {
		fmt.Fprintf(os.Stderr, "sandbox never exec-ready\n")
		os.Exit(2)
	}
	fmt.Printf("exec-rdy  @ %s\n", time.Now().Format("15:04:05.000"))

	// 3b. Mimic devm's ReconcilePortsWithRunner: list current ports
	// BEFORE publish (the currentMappings call).
	tList := time.Now()
	listOut, _ := exec.Command("sbx", "ports", name, "--json").Output()
	fmt.Printf("pre-list  @ %s dur=%s out=%s\n",
		time.Now().Format("15:04:05.000"),
		time.Since(tList).Round(time.Millisecond),
		strings.TrimSpace(string(listOut)))

	// 4. Publish (retry on no-container-endpoint).
	deadline := time.Now().Add(15 * time.Second)
	pubAttempt := 0
	for time.Now().Before(deadline) {
		pubAttempt++
		t := time.Now()
		out, err := exec.Command("sbx", "ports", name, "--publish", spec).CombinedOutput()
		fmt.Printf("publish#%d @ %s dur=%s rc=%v out=%q\n",
			pubAttempt, time.Now().Format("15:04:05.000"),
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

	// 4a. Mimic devm verifyMappingVisible: tight-poll sbx ports --json
	// every 250ms for up to 3s, then 500ms hold, then poll again 250ms.
	tightStart := time.Now()
	visibleSeen := false
	for time.Since(tightStart) < 3*time.Second {
		if portVisible(name) {
			visibleSeen = true
			fmt.Printf("verify1   @ %s (first visible after %s)\n",
				time.Now().Format("15:04:05.000"),
				time.Since(tightStart).Round(time.Millisecond))
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !visibleSeen {
		fmt.Println("verify1: NEVER VISIBLE in 3s")
	}
	// Hold 500ms then reverify
	time.Sleep(500 * time.Millisecond)
	stillVisible := portVisible(name)
	fmt.Printf("verify2   @ %s (after 500ms hold) still_visible=%v\n",
		time.Now().Format("15:04:05.000"), stillVisible)

	// 4b. Mimic devm WriteSnapshot: sbx exec NAME sh -c "... base64 -d > FILE"
	// This runs immediately after publish, before the observation window.
	tSnap := time.Now()
	const snapContent = "# devm-applied-config\nschemaVersion: \"1\"\n"
	const snapPath = "/home/agent/.devm/applied.yaml"
	b64 := encodeBase64(snapContent)
	snapCmd := fmt.Sprintf("mkdir -p $(dirname %s) && echo %s | base64 -d > %s.tmp && mv %s.tmp %s",
		snapPath, b64, snapPath, snapPath, snapPath)
	snapOut, snapErr := exec.Command("sbx", "exec", name, "sh", "-c", snapCmd).CombinedOutput()
	fmt.Printf("snapshot  @ %s dur=%s rc=%v out=%q\n",
		time.Now().Format("15:04:05.000"),
		time.Since(tSnap).Round(time.Millisecond), snapErr, snapOut)

	// 4c. Mimic devm: spawn user shell via `sbx exec -it bash`. Don't
	// block on it — just spawn and let it run while we observe the port.
	// stdin is set to a pipe so the shell stays alive (waiting for input).
	userShell := exec.Command("sbx", "exec", "-it", name, "bash")
	userShell.Stdin = nil
	userShell.Stdout = nil
	userShell.Stderr = nil
	if err := userShell.Start(); err != nil {
		fmt.Printf("user-shell spawn err: %v\n", err)
	} else {
		fmt.Printf("user-shell @ %s pid=%d\n",
			time.Now().Format("15:04:05.000"), userShell.Process.Pid)
	}
	defer func() {
		if userShell.Process != nil {
			_ = userShell.Process.Kill()
		}
	}()

	// 5. Poll for 10s.
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
	// Account for the final span.
	if prevPresent {
		visibleSeconds += time.Since(lastT).Seconds()
	}

	fmt.Printf("\ntotal_visible_seconds=%.2f (need %.2f for PASS)\n",
		visibleSeconds, stableNeed)
	if visibleSeconds >= stableNeed {
		fmt.Println("RESULT: PASS — mapping stable")
		os.Exit(0)
	}
	fmt.Println("RESULT: FAIL — mapping vanished (phantom-like)")
	os.Exit(1)
}

func waitFor(label string, timeout time.Duration, check func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

func matchesLine(out, name, status string) bool {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, name) && strings.Contains(line, status) {
			return true
		}
	}
	return false
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
