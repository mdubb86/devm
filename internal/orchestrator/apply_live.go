package orchestrator

import (
	"fmt"
	"strconv"

	"github.com/mtwaage/devm/internal/sandbox"
)

// ApplyLive runs every BucketLive change through the corresponding sbx
// command. Non-LIVE changes in the slice are skipped silently (caller
// is expected to handle them via the recreate path). portOffset is the
// project's port_offset, used to compute the host port for each
// canonical port (host = offset + canonical).
//
// Returns the first error encountered; later changes are not attempted
// after a failure so the snapshot stays coherent on retry.
func ApplyLive(sb *sandbox.Sandbox, changes []Change, portOffset int) error {
	for _, c := range changes {
		if c.Bucket() != BucketLive {
			continue
		}
		switch c.Kind {
		case KindPortAdd:
			sandboxPort, err := strconv.Atoi(c.Key)
			if err != nil {
				return fmt.Errorf("apply_live: port_add: bad sandbox port %q: %w", c.Key, err)
			}
			spec := fmt.Sprintf("127.0.0.1:%d:%d", portOffset+sandboxPort, sandboxPort)
			if err := sb.Runner.Run("sbx", "ports", sb.Name, "--publish", spec); err != nil {
				return fmt.Errorf("apply_live: sbx ports --publish %s: %w", spec, err)
			}
		case KindPortRemove:
			sandboxPort, err := strconv.Atoi(c.Key)
			if err != nil {
				return fmt.Errorf("apply_live: port_remove: bad sandbox port %q: %w", c.Key, err)
			}
			spec := fmt.Sprintf("127.0.0.1:%d:%d", portOffset+sandboxPort, sandboxPort)
			if err := sb.Runner.Run("sbx", "ports", sb.Name, "--unpublish", spec); err != nil {
				return fmt.Errorf("apply_live: sbx ports --unpublish %s: %w", spec, err)
			}
		case KindPortChange:
			oldP, err := strconv.Atoi(c.Old)
			if err != nil {
				return fmt.Errorf("apply_live: port_change: bad old port %q: %w", c.Old, err)
			}
			newP, err := strconv.Atoi(c.New)
			if err != nil {
				return fmt.Errorf("apply_live: port_change: bad new port %q: %w", c.New, err)
			}
			oldSpec := fmt.Sprintf("127.0.0.1:%d:%d", portOffset+oldP, oldP)
			newSpec := fmt.Sprintf("127.0.0.1:%d:%d", portOffset+newP, newP)
			if err := sb.Runner.Run("sbx", "ports", sb.Name, "--unpublish", oldSpec); err != nil {
				return fmt.Errorf("apply_live: port_change: unpublish %s: %w", oldSpec, err)
			}
			if err := sb.Runner.Run("sbx", "ports", sb.Name, "--publish", newSpec); err != nil {
				return fmt.Errorf("apply_live: port_change: publish %s: %w", newSpec, err)
			}
		case KindNetworkAdd:
			if err := sb.Runner.Run("sbx", "policy", "allow", "network", c.Key); err != nil {
				return fmt.Errorf("apply_live: sbx policy allow network %s: %w", c.Key, err)
			}
		}
	}
	return nil
}
