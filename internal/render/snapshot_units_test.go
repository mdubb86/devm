package render

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderSnapshotService_HappyPath(t *testing.T) {
	body := RenderSnapshotService("pg")
	s := string(body)
	assert.Contains(t, s, "Type=oneshot")
	assert.Contains(t, s, "ExecStart=/usr/local/bin/gdevm snapshot create pg --source cron")
	assert.Contains(t, s, "devm snapshot capture for pg")
	// Oneshot services activated by a timer must not carry an [Install]
	// section — enabling the .service directly would race with the
	// timer, or worse, run at boot.
	assert.NotContains(t, s, "[Install]")
}

func TestRenderSnapshotTimer_HappyPath(t *testing.T) {
	body, err := RenderSnapshotTimer("pg", "0 3 * * *")
	require.NoError(t, err)
	s := string(body)
	assert.Contains(t, s, "OnCalendar=*-*-* 03:00:00")
	assert.Contains(t, s, "Persistent=false")
	assert.Contains(t, s, "Unit=devm-snapshot-pg.service")
	assert.Contains(t, s, "WantedBy=timers.target")
	assert.Contains(t, s, "[Install]")
	// Timer unit itself needs to be enabled, so it must have an
	// [Install] section pointing at timers.target.
}

func TestRenderSnapshotTimer_RejectsBadCron(t *testing.T) {
	_, err := RenderSnapshotTimer("pg", "@hourly")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "@hourly")
}

func TestRenderSnapshotUnits_NoTrailingGarbage(t *testing.T) {
	// A trailing stray character or missing newline tripped systemd-
	// analyze unit-files on earlier devm unit renderers. Pin that both
	// outputs end with a newline and have no stray template delimiters.
	service := string(RenderSnapshotService("redis"))
	assert.True(t, strings.HasSuffix(service, "\n"), "service must end with newline")
	assert.NotContains(t, service, "{{")
	assert.NotContains(t, service, "}}")

	timer, err := RenderSnapshotTimer("redis", "*/5 * * * *")
	require.NoError(t, err)
	t2 := string(timer)
	assert.True(t, strings.HasSuffix(t2, "\n"), "timer must end with newline")
	assert.NotContains(t, t2, "{{")
	assert.NotContains(t, t2, "}}")
}
