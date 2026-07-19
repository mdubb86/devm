package schema

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseScriptRef(t *testing.T) {
	cases := []struct {
		in      string
		wantRef bool
		wantN   string
	}{
		{">install-supabase", true, "install-supabase"},
		{"> install-supabase", true, "install-supabase"},
		{"  > install-supabase  ", true, "install-supabase"},
		{"install-supabase", false, ""},
		{"echo hi > /tmp/out", false, ""},
		{"", false, ""},
		{">", true, ""}, // caught later by ValidateScriptName
	}
	for _, c := range cases {
		gotN, gotRef := ParseScriptRef(c.in)
		assert.Equal(t, c.wantRef, gotRef, "input %q", c.in)
		assert.Equal(t, c.wantN, gotN, "input %q", c.in)
	}
}

func TestValidateScriptName(t *testing.T) {
	valid := []string{"a", "install-supabase", "a1", "abc-def-ghi", "x1-y2"}
	invalid := []string{"", "-abc", "1abc", "ABC", "abc_def", "abc.def", "abc/def", " abc", "abc "}
	for _, s := range valid {
		assert.NoError(t, ValidateScriptName(s), "want valid: %q", s)
	}
	for _, s := range invalid {
		assert.Error(t, ValidateScriptName(s), "want invalid: %q", s)
	}
}
