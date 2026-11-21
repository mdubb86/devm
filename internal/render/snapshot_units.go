package render

import (
	"fmt"

	"github.com/mdubb86/devm/internal/cronspec"
	"github.com/mdubb86/devm/internal/schema"
)

// RenderSnapshotService returns the systemd oneshot service body that
// runs `gdevm snapshot create <entryName>`. The matching timer
// (RenderSnapshotTimer) activates this service; the service itself
// has no [Install] section, so enabling the timer is what makes the
// snapshot fire.
func RenderSnapshotService(entryName string) []byte {
	return []byte(fmt.Sprintf(`[Unit]
Description=devm snapshot capture for %s

[Service]
Type=oneshot
ExecStart=/usr/local/bin/gdevm snapshot create %s --source cron
`, entryName, entryName))
}

// RenderSnapshotTimer returns the systemd timer unit that activates
// devm-snapshot-<entryName>.service on the schedule translated from
// cronSpec. Persistent=false — a missed firing (VM was stopped) stays
// missed; the next scheduled run is what catches up.
//
// Returns an error if cronspec.ToOnCalendar rejects cronSpec.
func RenderSnapshotTimer(entryName, cronSpec string) ([]byte, error) {
	onCalendar, err := cronspec.ToOnCalendar(cronSpec)
	if err != nil {
		return nil, fmt.Errorf("render snapshot timer %s: %w", entryName, err)
	}
	return []byte(fmt.Sprintf(`[Unit]
Description=devm snapshot timer for %s

[Timer]
OnCalendar=%s
Persistent=false
Unit=%s%s.service

[Install]
WantedBy=timers.target
`, entryName, onCalendar, schema.SnapshotUnitPrefix, entryName)), nil
}
