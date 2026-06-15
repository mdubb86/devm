package orchestrator

import (
	"strings"
	"testing"
)

func TestShortenForSpinner_StripsBackslashNewlineAndCollapses(t *testing.T) {
	in := `ver=$(curl -fsSL https://api.github.com/repos/supabase/cli/releases/latest \
  | sed -n 's/.*"tag_name"//p') && \
curl -fsSL -o /tmp/supabase.deb \
  "https://github.com/supabase/cli/releases/download/${ver}/x.deb" && \
dpkg -i /tmp/supabase.deb`
	got := shortenForSpinner(in, 100)
	if strings.ContainsAny(got, "\n\\") {
		t.Errorf("expected no newlines or trailing backslashes, got %q", got)
	}
	if len(got) > 100 {
		t.Errorf("expected length <= 100, got %d", len(got))
	}
	if !strings.HasPrefix(got, "ver=$(curl") {
		t.Errorf("expected head 'ver=$(curl' preserved, got %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected ellipsis on overflow, got %q", got)
	}
}

func TestShortenForSpinner_LeavesShortStringsAlone(t *testing.T) {
	got := shortenForSpinner("apt-get install -y jq", 100)
	if got != "apt-get install -y jq" {
		t.Errorf("expected unchanged, got %q", got)
	}
}

func TestShortenForSpinner_MaxZeroDisablesTruncation(t *testing.T) {
	in := "this is a very long string that would normally get truncated"
	got := shortenForSpinner(in, 0)
	if got != in {
		t.Errorf("max=0 must not truncate, got %q", got)
	}
}

func TestShortenForSpinner_MultilineWithoutBackslashContinuation(t *testing.T) {
	in := "echo one\necho two\necho three"
	got := shortenForSpinner(in, 100)
	if strings.Contains(got, "\n") {
		t.Errorf("expected no newlines, got %q", got)
	}
	if got != "echo one echo two echo three" {
		t.Errorf("expected lines joined with spaces, got %q", got)
	}
}
