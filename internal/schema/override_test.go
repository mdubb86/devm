package schema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"gopkg.in/yaml.v3"
)

func TestConfigOverridePartial_Proxy(t *testing.T) {
	src := []byte(`
project:
  proxy: none
`)
	var o ConfigOverride
	err := yaml.Unmarshal(src, &o)
	assert.NoError(t, err)
	assert.NotNil(t, o.Project)
	assert.Equal(t, "none", *o.Project.Proxy)
}
