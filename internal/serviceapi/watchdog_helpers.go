package serviceapi

import (
	"os"
	"sort"
	"strings"

	"github.com/mdubb86/devm/internal/daemonlog"
	"github.com/mdubb86/devm/internal/identity"
)

// KnownProjectNamesForWatchdog enumerates every persisted per-project
// state snapshot in StateDir(cfg) and returns their project names,
// sorted for deterministic iteration. Mirrors the directory walk in
// listProjectStatuses (statusall.go), minus the VM/proxy joins that
// handler needs — the watchdog only needs the project name set so it
// knows which rows to touch.
func KnownProjectNamesForWatchdog(cfg identity.Config) []string {
	entries, err := os.ReadDir(StateDir(cfg))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		daemonlog.Errorf("serviceapi: watchdog: list known projects: %v", err)
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		names = append(names, strings.TrimSuffix(name, ".json"))
	}
	sort.Strings(names)
	return names
}
