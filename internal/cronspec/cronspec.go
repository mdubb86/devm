package cronspec

import (
	"fmt"
	"strconv"
	"strings"
)

// ToOnCalendar translates a standard five-field cron expression
// ("minute hour dayOfMonth month dayOfWeek") to systemd's OnCalendar=
// grammar. The output form is `[Dow *-]MM-DD HH:MM:00`, where any
// field that was `*` stays `*` in-place; the day-of-week prefix only
// appears when that field is not `*`.
//
// Accepted grammar per field: integer literals, `*`, `*/N` steps,
// `a,b,c` enumerations, `a-b` ranges, plus lowercase day names
// `mon`-`sun` (DOW only) and lowercase month names `jan`-`dec` (month
// only). Macros like `@hourly` are not supported in v1 and return an
// error naming the input.
func ToOnCalendar(spec string) (string, error) {
	if strings.TrimSpace(spec) == "" {
		return "", fmt.Errorf("cron spec %q: empty", spec)
	}
	fields := strings.Fields(spec)
	if len(fields) != 5 {
		return "", fmt.Errorf("cron spec %q: expected 5 whitespace-separated fields, got %d", spec, len(fields))
	}

	min, err := translateField(fields[0], fieldMinute)
	if err != nil {
		return "", fmt.Errorf("cron spec %q: minute: %w", spec, err)
	}
	hour, err := translateField(fields[1], fieldHour)
	if err != nil {
		return "", fmt.Errorf("cron spec %q: hour: %w", spec, err)
	}
	dom, err := translateField(fields[2], fieldDayOfMonth)
	if err != nil {
		return "", fmt.Errorf("cron spec %q: day-of-month: %w", spec, err)
	}
	mon, err := translateField(fields[3], fieldMonth)
	if err != nil {
		return "", fmt.Errorf("cron spec %q: month: %w", spec, err)
	}
	dow, err := translateField(fields[4], fieldDayOfWeek)
	if err != nil {
		return "", fmt.Errorf("cron spec %q: day-of-week: %w", spec, err)
	}

	datePart := mon + "-" + dom
	timePart := hour + ":" + min + ":00"
	out := "*-" + datePart + " " + timePart
	if fields[4] != "*" {
		out = dow + " " + out
	}
	return out, nil
}

// fieldKind names the cron field position so translateField knows which
// value range and name-set applies.
type fieldKind int

const (
	fieldMinute fieldKind = iota
	fieldHour
	fieldDayOfMonth
	fieldMonth
	fieldDayOfWeek
)

// fieldBounds returns the inclusive numeric range for a cron field.
func fieldBounds(k fieldKind) (int, int) {
	switch k {
	case fieldMinute:
		return 0, 59
	case fieldHour:
		return 0, 23
	case fieldDayOfMonth:
		return 1, 31
	case fieldMonth:
		return 1, 12
	case fieldDayOfWeek:
		return 0, 7
	}
	return 0, 0
}

// dowNames maps lowercase day-of-week names to their Title-Case output
// form. Numeric values 0-7 (0 and 7 both = Sunday) are accepted by the
// numeric parser; names are translated here.
var dowNames = map[string]string{
	"mon": "Mon",
	"tue": "Tue",
	"wed": "Wed",
	"thu": "Thu",
	"fri": "Fri",
	"sat": "Sat",
	"sun": "Sun",
}

// monthNumbers maps lowercase month names to their two-digit numeric
// form. The named form is translated to numeric in the OnCalendar=
// output so a single date-triplet shape covers both inputs.
var monthNumbers = map[string]string{
	"jan": "01",
	"feb": "02",
	"mar": "03",
	"apr": "04",
	"may": "05",
	"jun": "06",
	"jul": "07",
	"aug": "08",
	"sep": "09",
	"oct": "10",
	"nov": "11",
	"dec": "12",
}

// translateField translates one cron field to its OnCalendar= form.
// The translation preserves wildcards, step/range/list structure, and
// pads integer literals to two digits for the time fields; named
// months become numeric and named days become Title Case.
func translateField(raw string, kind fieldKind) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("empty field")
	}
	// Enumerated list: translate each element independently.
	if strings.Contains(raw, ",") {
		parts := strings.Split(raw, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			t, err := translateField(p, kind)
			if err != nil {
				return "", err
			}
			out = append(out, t)
		}
		return strings.Join(out, ","), nil
	}
	// Step: `*/N` or `a-b/N`.
	if idx := strings.Index(raw, "/"); idx >= 0 {
		head := raw[:idx]
		stepStr := raw[idx+1:]
		if head != "*" {
			return "", fmt.Errorf("unsupported step base %q (only `*/N` is supported in v1)", raw)
		}
		step, err := strconv.Atoi(stepStr)
		if err != nil || step <= 0 {
			return "", fmt.Errorf("step %q: must be a positive integer", stepStr)
		}
		// systemd grammar: `*:0/N:00` for every N minutes — the base of
		// the step is 0, printed explicitly. Applies uniformly across
		// fields: a `*/N` wildcard-with-step keeps the leading 0 base.
		return "0/" + strconv.Itoa(step), nil
	}
	// Range: `a-b`.
	if idx := strings.Index(raw, "-"); idx >= 0 {
		lo := raw[:idx]
		hi := raw[idx+1:]
		lot, err := translateField(lo, kind)
		if err != nil {
			return "", err
		}
		hit, err := translateField(hi, kind)
		if err != nil {
			return "", err
		}
		return lot + ".." + hit, nil
	}
	// Wildcard.
	if raw == "*" {
		return "*", nil
	}
	// Named day-of-week.
	if kind == fieldDayOfWeek {
		if name, ok := dowNames[strings.ToLower(raw)]; ok {
			return name, nil
		}
	}
	// Named month.
	if kind == fieldMonth {
		if n, ok := monthNumbers[strings.ToLower(raw)]; ok {
			return n, nil
		}
	}
	// Numeric literal.
	n, err := strconv.Atoi(raw)
	if err != nil {
		return "", fmt.Errorf("unrecognized field %q", raw)
	}
	lo, hi := fieldBounds(kind)
	if n < lo || n > hi {
		return "", fmt.Errorf("value %d out of range [%d,%d]", n, lo, hi)
	}
	// Minute / hour / day-of-month / month all get a two-digit numeric
	// form. Day-of-week numerics are preserved but zero-padded to one
	// digit (0 stays 0); systemd accepts numeric DOW as-is, but in
	// practice names cover every DOW case we emit — the numeric path
	// just keeps the value readable.
	switch kind {
	case fieldMinute, fieldHour, fieldDayOfMonth, fieldMonth:
		return fmt.Sprintf("%02d", n), nil
	default:
		return strconv.Itoa(n), nil
	}
}
