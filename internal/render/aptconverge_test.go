package render

import (
	"strings"
	"testing"
)

func TestAptConvergeScript_AddsAndRemoves(t *testing.T) {
	s := AptConvergeScript([]string{"sl", "jq"}, []string{"chromium"})
	for _, want := range []string{
		"sudo dpkg --configure -a",
		"apt_run update",
		"apt_run install -y 'sl' 'jq'",
		"apt_run remove -y 'chromium'",
		"apt_run autoremove -y",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script missing %q:\n%s", want, s)
		}
	}
}

func TestAptConvergeScript_DpkgConfigureIsFirstLine(t *testing.T) {
	s := AptConvergeScript([]string{"sl"}, nil)
	lines := strings.Split(s, "\n")
	if len(lines) == 0 || lines[0] != "sudo dpkg --configure -a" {
		t.Errorf("want first line to be dpkg self-repair, got script:\n%s", s)
	}
}

func TestAptConvergeScript_AddOnly_NoRemoveStages(t *testing.T) {
	s := AptConvergeScript([]string{"sl"}, nil)
	if strings.Contains(s, "remove") || strings.Contains(s, "autoremove") {
		t.Errorf("add-only script must not remove:\n%s", s)
	}
}

func TestAptConvergeScript_RemoveOnly_NoUpdateNoInstall(t *testing.T) {
	s := AptConvergeScript(nil, []string{"sl"})
	if strings.Contains(s, "update") || strings.Contains(s, "install -y") {
		t.Errorf("remove-only script must not update/install:\n%s", s)
	}
}

func TestAptConvergeScript_Empty(t *testing.T) {
	s := AptConvergeScript(nil, nil)
	if s != "" {
		t.Errorf("want empty, got %q", s)
	}
	if strings.Contains(s, "dpkg --configure") {
		t.Errorf("empty script must not carry the dpkg self-repair line:\n%s", s)
	}
}
