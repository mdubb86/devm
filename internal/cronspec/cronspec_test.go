package cronspec

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToOnCalendar_FiveFieldStar(t *testing.T) {
	got, err := ToOnCalendar("* * * * *")
	require.NoError(t, err)
	assert.Equal(t, "*-*-* *:*:00", got)
}

func TestToOnCalendar_DailyAt3AM(t *testing.T) {
	got, err := ToOnCalendar("0 3 * * *")
	require.NoError(t, err)
	assert.Equal(t, "*-*-* 03:00:00", got)
}

func TestToOnCalendar_Every5Min(t *testing.T) {
	got, err := ToOnCalendar("*/5 * * * *")
	require.NoError(t, err)
	assert.Equal(t, "*-*-* *:0/5:00", got)
}

func TestToOnCalendar_MonAt9AM(t *testing.T) {
	got, err := ToOnCalendar("0 9 * * mon")
	require.NoError(t, err)
	assert.Equal(t, "Mon *-*-* 09:00:00", got)
}

func TestToOnCalendar_MonWedAt9AM(t *testing.T) {
	// Enumerated day-of-week list; the day prefix appears before the
	// date triplet when DOW is set.
	got, err := ToOnCalendar("0 9 * * mon,wed")
	require.NoError(t, err)
	assert.Equal(t, "Mon,Wed *-*-* 09:00:00", got)
}

func TestToOnCalendar_NamedMonthToNumeric(t *testing.T) {
	// Named month ("jul") translates to the numeric two-digit form.
	got, err := ToOnCalendar("0 3 1 jul *")
	require.NoError(t, err)
	assert.Equal(t, "*-07-01 03:00:00", got)
}

func TestToOnCalendar_RangeAndList(t *testing.T) {
	// Range uses `..`; list stays as `,`. Numeric literals in minute
	// and hour are two-digit zero-padded uniformly, including the
	// endpoints of a range.
	got, err := ToOnCalendar("0-5 1,13 * * *")
	require.NoError(t, err)
	assert.Equal(t, "*-*-* 01,13:00..05:00", got)
}

func TestToOnCalendar_RejectsSixFields(t *testing.T) {
	_, err := ToOnCalendar("0 3 * * * *")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "0 3 * * * *")
}

func TestToOnCalendar_RejectsHourlyMacro(t *testing.T) {
	_, err := ToOnCalendar("@hourly")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "@hourly")
}

func TestToOnCalendar_RejectsEmpty(t *testing.T) {
	_, err := ToOnCalendar("")
	require.Error(t, err)
}

func TestToOnCalendar_RejectsGarbage(t *testing.T) {
	_, err := ToOnCalendar("wat")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wat")
}

func TestToOnCalendar_RejectsBadCharacter(t *testing.T) {
	// `?` is non-standard cron; reject it.
	_, err := ToOnCalendar("? 3 * * *")
	require.Error(t, err)
}

func TestToOnCalendar_RejectsOutOfRangeMinute(t *testing.T) {
	_, err := ToOnCalendar("60 3 * * *")
	require.Error(t, err)
}
