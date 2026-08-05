package schema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"gopkg.in/yaml.v3"
)

func TestConfigOverridePartial_ProjectName(t *testing.T) {
	src := []byte(`
project:
  name: overridden
`)
	var o ConfigOverride
	err := yaml.Unmarshal(src, &o)
	assert.NoError(t, err)
	assert.NotNil(t, o.Project)
	assert.Equal(t, "overridden", *o.Project.Name)
}
