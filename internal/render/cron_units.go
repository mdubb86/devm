package render

import (
	"fmt"

	"github.com/mdubb86/devm/internal/cronspec"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/scriptfile"
)

// CronWrapperGuestDir is the guest-side directory holding one wrapper
// script per cron entry. A cron service's ExecStart= points at
// <CronWrapperGuestDir>/<fn>.sh.
const CronWrapperGuestDir = "/opt/devm/cron-wrappers"

// CronServiceUnit is the systemd service unit name for a cron entry.
func CronServiceUnit(fn string) string { return schema.CronUnitPrefix + fn + ".service" }

// CronTimerUnit is the systemd timer unit name for a cron entry.
func CronTimerUnit(fn string) string { return schema.CronUnitPrefix + fn + ".timer" }

// CronWrapperPath is the guest path of a cron entry's wrapper script.
func CronWrapperPath(fn string) string { return CronWrapperGuestDir + "/" + fn + ".sh" }

// RenderCronJob returns the oneshot service, the timer, and the wrapper
// script for one cron entry: the timer fires the service on the schedule
// translated from spec, and the service runs the wrapper, which sources
// devm.sh and calls fn.
//
// The timer has no [Install] section on purpose: cron timers are only
// ever started, never enabled, so they cannot re-arm on reboot before
// network enforcement is in place.
//
// The wrapper is built exactly as RenderService builds a function-form
// service's wrapper, so both run functions in the same environment.
func RenderCronJob(fn, spec string, env map[string]string, pathPrepend []string) (service, timer, wrapper []byte, err error) {
	onCalendar, err := cronspec.ToOnCalendar(spec)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("render cron job %q: %w", fn, err)
	}
	body, err := scriptfile.RenderWrapper(scriptfile.WrapperInput{
		FunctionName: fn,
		Env:          env,
		PathPrepend:  pathPrepend,
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("render cron job %q wrapper: %w", fn, err)
	}
	service = []byte(fmt.Sprintf(`[Unit]
Description=devm cron: %s
After=devm-ready.target
Requires=devm-ready.target

[Service]
Type=oneshot
User=devm
EnvironmentFile=-/etc/environment
ExecStart=%s
`, fn, CronWrapperPath(fn)))
	timer = []byte(fmt.Sprintf(`[Unit]
Description=devm cron timer: %s

[Timer]
OnCalendar=%s
Persistent=false
Unit=%s
`, fn, onCalendar, CronServiceUnit(fn)))
	return service, timer, []byte(body), nil
}
