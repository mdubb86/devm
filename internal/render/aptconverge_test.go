package render

import (
	"strings"
	"testing"
)

func TestAptConvergeScript_AddsAndRemoves(t *testing.T) {
	s := AptConvergeScript([]string{"sl", "jq"}, []string{"chromium"})
	for _, want := range []string{
		"apt-get -o DPkg::Lock::Timeout=60 update",
		"apt-get -o DPkg::Lock::Timeout=60 install -y 'sl' 'jq'",
		"apt-get -o DPkg::Lock::Timeout=60 remove -y 'chromium'",
		"apt-get -o DPkg::Lock::Timeout=60 autoremove -y",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script missing %q:\n%s", want, s)
		}
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
	if s := AptConvergeScript(nil, nil); s != "" {
		t.Errorf("want empty, got %q", s)
	}
}
