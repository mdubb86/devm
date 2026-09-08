package serviceapi

import (
	"context"
	"fmt"

	"github.com/mdubb86/devm/internal/daemonlog"
)

type vmCheck struct{}

func NewVMCheck() Check { return &vmCheck{} }

func (vmCheck) Name() string { return "vm" }

func (vmCheck) Run(ctx context.Context, cache *StateCache, gt GroundTruth) (bool, error) {
	vms, err := gt.TartList(ctx)
	if err != nil {
		return false, fmt.Errorf("vm check: tart list: %w", err)
	}
	observed := make(map[string]VMState, len(vms))
	for _, vm := range vms {
		if vm.Running {
			observed[vm.Name] = VMRunning
		} else {
			observed[vm.Name] = VMStopped
		}
	}
	driftedAny := false
	for _, projectID := range gt.KnownProjectNames() {
		expected, _ := cache.ProjectRow(projectID)
		obs, present := observed[projectID]
		if !present {
			obs = VMAbsent
		}
		if obs == expected.VMState {
			cache.TouchProjectReconciled(projectID)
			continue
		}
		driftedAny = true
		daemonlog.Warnf("watchdog: drift on vm for %s: %s → %s, cache reconciled to observed",
			projectID, expected.VMState, obs)
		cache.SetVMState(projectID, obs)
		cache.TouchProjectReconciled(projectID)
	}
	return driftedAny, nil
}
