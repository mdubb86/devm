package serviceapi

import (
	"bufio"
	"context"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mdubb86/devm/internal/debuglog"
	"github.com/mdubb86/devm/internal/identity"
)

// orphanSoftnetKillGrace bounds how long ReapOrphanSoftnets waits after
// SIGTERM before escalating a surviving orphan to SIGKILL.
const orphanSoftnetKillGrace = 2 * time.Second

// ReapOrphanSoftnets kills every softnet process (see ensureSoftnetSymlink)
// whose parent has already exited — PPID == 1, reparented to launchd/init.
//
// softnet is a child `tart run --net-softnet` forks internally, not a
// process the supervisor tracks directly (see shutdownSoftnet in vm.go);
// /vm/stop now asks it to exit over its control socket, and softnet's own
// shutdown handling reliably unblocks and exits (internal/softnet/softnet.go,
// acceptUntilShutdown). But that only covers a clean /vm/stop. A daemon
// that's killed or crashes mid-project — before it reaches that code, or
// before this daemon instance even existed — leaves softnet running with
// nothing left to ask it to stop, and no supervisor record of it either.
//
// PPID == 1 is a safe, sufficient signal that a softnet is orphaned: a
// softnet still serving a live VM always has a live `tart run` parent
// (its own process, running for as long as the VM is up), so this never
// touches one that's still in use — it only ever kills processes whose
// owning `tart run` has already exited out from under them, which is
// exactly the leak this reaps.
//
// Runs in the background (non-blocking): daemon startup must not stall on
// `ps` or on waiting out the SIGKILL escalation grace window.
func ReapOrphanSoftnets(ctx context.Context, cfg identity.Config) {
	go func() {
		binary := filepath.Join(cfg.RuntimeDir(), "softnet-bin", "softnet")
		out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,command=").Output()
		if err != nil {
			debuglog.Logf("serviceapi", "reap orphan softnets: ps: %v", err)
			return
		}
		pids := parseOrphanSoftnets(string(out), binary)
		if len(pids) == 0 {
			return
		}
		debuglog.Logf("serviceapi", "reaping %d orphan softnet process(es) (parent already exited): %v", len(pids), pids)
		for _, pid := range pids {
			_ = syscall.Kill(pid, syscall.SIGTERM)
		}
		time.Sleep(orphanSoftnetKillGrace)
		for _, pid := range pids {
			if err := syscall.Kill(pid, 0); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	}()
}

// parseOrphanSoftnets extracts orphaned softnet PIDs from `ps -axo
// pid=,ppid=,command=` output: every line whose command starts with
// softnetBinary and whose ppid is 1. Split out from ReapOrphanSoftnets so
// tests don't have to shell out or spawn real processes.
func parseOrphanSoftnets(psOutput, softnetBinary string) []int {
	var out []int
	sc := bufio.NewScanner(strings.NewReader(psOutput))
	for sc.Scan() {
		line := strings.TrimLeft(sc.Text(), " ")
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		if ppid != 1 {
			continue // still owned by a live tart-run — never touch this one
		}
		command := strings.Join(fields[2:], " ")
		if !strings.HasPrefix(command, softnetBinary) {
			continue
		}
		out = append(out, pid)
	}
	return out
}
