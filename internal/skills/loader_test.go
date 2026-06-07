package skills

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixtureRaw is a small fake "embedded skill" used to test the parser
// without depending on the actual content under internal/skills/*.md.
const fixtureRaw = `---
name: example
description: An example skill for tests.
hidden: false
---

# Example body
Hello world.
`

func TestParseSkill_FrontmatterAndBody(t *testing.T) {
	s, err := parseSkill("example.md", fixtureRaw)
	require.NoError(t, err)
	assert.Equal(t, "example", s.Name)
	assert.Equal(t, "An example skill for tests.", s.Description)
	assert.False(t, s.Hidden)
	assert.Contains(t, s.Body, "Hello world.")
	assert.NotContains(t, s.Body, "---", "frontmatter must be stripped from body")
}

func TestParseSkill_MissingFrontmatterErrors(t *testing.T) {
	_, err := parseSkill("bad.md", "no frontmatter here\nfoo\n")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "frontmatter")
}

func TestParseSkill_MissingNameErrors(t *testing.T) {
	const raw = `---
description: missing name
---
body
`
	_, err := parseSkill("bad.md", raw)
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "name")
}

func TestParseSkill_HiddenTrueIsReadFromFrontmatter(t *testing.T) {
	const raw = `---
name: hidden-example
description: Hidden test.
hidden: true
---

body
`
	s, err := parseSkill("hidden.md", raw)
	require.NoError(t, err)
	assert.True(t, s.Hidden)
}

func TestParseSkill_EmptyFrontmatterErrors(t *testing.T) {
	// Frontmatter parses but produces zero-value struct, missing name → error.
	_, err := parseSkill("empty.md", "---\n---\nbody\n")
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "name")
}

func TestParseSkill_BodyTrimmingStripsLeadingBlankLines(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		wantBody string
	}{
		{
			name:     "no_blank_line",
			raw:      "---\nname: x\n---\nbody line\n",
			wantBody: "body line\n",
		},
		{
			name:     "one_blank_line",
			raw:      "---\nname: x\n---\n\nbody line\n",
			wantBody: "body line\n",
		},
		{
			name:     "two_blank_lines",
			raw:      "---\nname: x\n---\n\n\nbody line\n",
			wantBody: "body line\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := parseSkill("test.md", tc.raw)
			require.NoError(t, err)
			assert.Equal(t, tc.wantBody, s.Body)
		})
	}
}

func TestList_IncludesEmbeddedDevmSkill(t *testing.T) {
	skills, err := List()
	require.NoError(t, err)
	names := make([]string, 0, len(skills))
	for _, s := range skills {
		names = append(names, s.Name)
	}
	assert.Contains(t, names, "devm")
}

func TestGet_ReturnsBodyForKnownSkill(t *testing.T) {
	s, err := Get("devm")
	require.NoError(t, err)
	assert.NotEmpty(t, s.Body)
}

func TestGet_UnknownNameReturnsError(t *testing.T) {
	_, err := Get("nonexistent-skill")
	require.Error(t, err)
}
