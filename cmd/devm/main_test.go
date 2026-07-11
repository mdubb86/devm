package main

import "testing"

// TestSkipDriftCheckPaths_CoversKardianosSubcommands pins that
// _kardianos install / uninstall are drift-skipped. The install and
// uninstall flows shell out to these under sudo; without the skip
// entry, the child process would hit the drift check and exit before
// kardianos actually ran, leaving the user permanently unable to
// resync after a devm upgrade.
func TestSkipDriftCheckPaths_CoversKardianosSubcommands(t *testing.T) {
	must := []string{
		"devm install",
		"devm uninstall",
		"devm _kardianos install",
		"devm _kardianos uninstall",
	}
	for _, cmd := range must {
		if _, ok := skipDriftCheckPaths[cmd]; !ok {
			t.Errorf("skipDriftCheckPaths missing %q — install/uninstall would fail on drift", cmd)
		}
	}
}
