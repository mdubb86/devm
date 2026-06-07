package skills

import (
	"reflect"
	"strings"
	"testing"

	"github.com/mtwaage/devm/internal/schema"
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
