package skills

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSchemaSkillMentionsAllConfigFields fails when a new field is
// added to schema.Config but the schema.md reference forgets to
// mention it. The check is over `yaml:` tag names (the user-facing
// field names), not Go field names.
func TestSchemaSkillMentionsAllConfigFields(t *testing.T) {
	s, err := Get("schema")
	require.NoError(t, err)
	body := s.Body

	cfgType := reflect.TypeOf(schema.Config{})
	var missing []string
	for i := 0; i < cfgType.NumField(); i++ {
		f := cfgType.Field(i)
		tag := f.Tag.Get("yaml")
		// "name,omitempty" -> "name"
		name := strings.Split(tag, ",")[0]
		if name == "" || name == "-" {
			continue
		}
		if !strings.Contains(body, "`"+name+"`") {
			missing = append(missing, name)
		}
	}
	require.Empty(t, missing,
		"schema.md is missing references for these Config fields: %v "+
			"(add them to the schema cheatsheet)", missing)
}

// ---------------------------------------------------------------------------
// Retired-term helpers
// ---------------------------------------------------------------------------

// scanForRetiredTerms returns the list of retired terms found in body
// (case-insensitive whole-word match). Helper isolated so the regex
// logic can be table-tested independent of the embedded content scan.
func scanForRetiredTerms(body string, terms []string) []string {
	var hits []string
	lower := strings.ToLower(body)
	for _, t := range terms {
		// Whole-word match: `\bsbx\b` style, allowing `.`, `-`, `_`
		// inside the term itself (e.g. install-all-ok).
		re := regexp.MustCompile(`\b` + regexp.QuoteMeta(t) + `\b`)
		if re.MatchString(lower) {
			hits = append(hits, t)
		}
	}
	return hits
}

func TestScanForRetiredTerms_TableTests(t *testing.T) {
	terms := []string{"sbx", "allowed_domains", "wrap-fg", "kit policy"}
	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "clean",
			body: "VMs are provisioned by tart and iron-proxy enforces egress.",
			want: nil,
		},
		{
			name: "catches sbx",
			body: "Run sbx exec to debug.",
			want: []string{"sbx"},
		},
		{
			name: "catches sbx in any case",
			body: "The Sbx host-global defaults",
			want: []string{"sbx"},
		},
		{
			name: "doesn't catch sbx inside another word",
			body: "Use the sbxer tool",
			want: nil,
		},
		{
			name: "catches allowed_domains key",
			body: "Add allowed_domains: [...]",
			want: []string{"allowed_domains"},
		},
		{
			name: "catches wrap-fg",
			body: "wrap-fg.sh captures stderr",
			want: []string{"wrap-fg"},
		},
		{
			name: "multiple hits reported",
			body: "sbx exec and allowed_domains config",
			want: []string{"sbx", "allowed_domains"},
		},
		{
			name: "catches multi-word kit policy",
			body: "use kit policy when restricting network",
			want: []string{"kit policy"},
		},
		{
			name: "doesn't catch when kit and policy are separated",
			body: "use kit other words policy when restricting network",
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := scanForRetiredTerms(tc.body, terms)
			require.ElementsMatch(t, tc.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// Migration-note stripping
// ---------------------------------------------------------------------------

var migrationNoteRE = regexp.MustCompile(`(?s)<!-- migration-note-start -->.*?<!-- migration-note-end -->`)

// stripMigrationNotes removes all content between migration-note marker
// comments so the retired-term scan ignores intentional migration guidance.
func stripMigrationNotes(body string) string {
	return migrationNoteRE.ReplaceAllString(body, "")
}

func TestStripMigrationNotes_TableTests(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "no markers — body unchanged",
			body: "Clean content with no markers.",
			want: "Clean content with no markers.",
		},
		{
			name: "markers present — content between stripped",
			body: "before\n<!-- migration-note-start -->\nallowed_domains: old key\n<!-- migration-note-end -->\nafter",
			want: "before\n\nafter",
		},
		{
			name: "opening marker without closing — NOT stripped",
			body: "before\n<!-- migration-note-start -->\nallowed_domains: old key\nafter",
			want: "before\n<!-- migration-note-start -->\nallowed_domains: old key\nafter",
		},
		{
			name: "multiple marker pairs both stripped",
			body: "a\n<!-- migration-note-start -->X<!-- migration-note-end -->\nb\n<!-- migration-note-start -->Y<!-- migration-note-end -->\nc",
			want: "a\n\nb\n\nc",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := stripMigrationNotes(tc.body)
			assert.Equal(t, tc.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// Negative retired-term scan over embedded skills
// ---------------------------------------------------------------------------

var retiredTerms = []string{
	"sbx",
	"wrap-fg", "wrap-bg",
	"install-all-ok", "startup-all-ok",
	"allowed_domains",
	"sandbox_name",
	"kit-policy", "kit policy",
	"anchor process", "anchor-alive",
}

func TestNoRetiredTermsInSkills(t *testing.T) {
	skills, err := List()
	require.NoError(t, err)
	for _, s := range skills {
		body := stripMigrationNotes(s.Body)
		hits := scanForRetiredTerms(body, retiredTerms)
		assert.Empty(t, hits,
			"skill %q contains retired terms %v outside migration-note markers — these belong only in docs/superpowers/, not the embedded skill set",
			s.Name, hits)
	}
}

// ---------------------------------------------------------------------------
// Positive new-architecture floor
// ---------------------------------------------------------------------------

func TestNewArchitectureMentioned(t *testing.T) {
	requiredAny := []string{"tart", "iron-proxy", "daemon", "provision"}
	skills, err := List()
	require.NoError(t, err)
	var combined strings.Builder
	for _, s := range skills {
		combined.WriteString(strings.ToLower(s.Body))
		combined.WriteString("\n")
	}
	body := combined.String()
	for _, term := range requiredAny {
		assert.Contains(t, body, strings.ToLower(term),
			"no embedded skill mentions %q; the new architecture must be taught somewhere",
			term)
	}
}
