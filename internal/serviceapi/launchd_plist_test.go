package serviceapi

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLaunchdPlistTemplate_ContainsRequiredKeys(t *testing.T) {
	plist := strings.NewReplacer(
		"__LOG_OUT__", "/Users/alice/Library/Logs/com.devm.service.out.log",
		"__LOG_ERR__", "/Users/alice/Library/Logs/com.devm.service.err.log",
		"__HOME__", "/Users/alice",
		"__USER__", "alice",
	).Replace(LaunchdPlistTemplate)

	assert.Contains(t, plist, "<key>UserName</key>")
	assert.Contains(t, plist, "<string>alice</string>")
	assert.Contains(t, plist, "<key>EnvironmentVariables</key>")
	assert.Contains(t, plist, "<string>/Users/alice</string>")
	assert.Contains(t, plist, "<key>PATH</key>")
	assert.Contains(t, plist, "<key>HTTPSocket</key>")
	assert.Contains(t, plist, "<string>80</string>")
	assert.Contains(t, plist, "<key>HTTPSSocket</key>")
	assert.Contains(t, plist, "<string>443</string>")
	assert.Contains(t, plist, "<string>/Users/alice/Library/Logs/com.devm.service.out.log</string>")
	assert.Contains(t, plist, "<string>/Users/alice/Library/Logs/com.devm.service.err.log</string>")
	assert.Contains(t, plist, "<key>KeepAlive</key>")
	assert.Contains(t, plist, "<key>RunAtLoad</key>")

	for _, ph := range []string{"__USER__", "__HOME__", "__LOG_OUT__", "__LOG_ERR__"} {
		assert.NotContains(t, plist, ph, "placeholder %s not substituted", ph)
	}
}
