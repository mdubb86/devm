package schema

import (
	"fmt"
	"sort"

	"github.com/mdubb86/devm/internal/cronspec"
)

// validateCron checks every cron entry: the key names a devm.sh function
// (not one devm already runs itself) and the value is a five-field
// cron expression. Keys are sorted so the first error is deterministic.
func (c Config) validateCron() error {
	funcSet := make(map[string]bool, len(c.Functions))
	for _, f := range c.Functions {
		funcSet[f] = true
	}
	names := make([]string, 0, len(c.Cron))
	for name := range c.Cron {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if name == "install" || name == "startup" {
			return fmt.Errorf("cron.%s: %q runs automatically and cannot be scheduled", name, name)
		}
		if !funcSet[name] {
			return fmt.Errorf("cron.%s: function %q is not defined in devm.sh (or devm.me.sh)", name, name)
		}
		if _, err := cronspec.ToOnCalendar(c.Cron[name]); err != nil {
			return fmt.Errorf("cron.%s: %w", name, err)
		}
	}
	return nil
}
