package release

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClassify_BrewPrefixes(t *testing.T) {
	cases := []string{
		"/opt/homebrew/Cellar/devm/0.1.0/bin/devm",
		"/usr/local/Cellar/devm/0.1.0/bin/devm",
		"/home/linuxbrew/.linuxbrew/Cellar/devm/0.1.0/bin/devm",
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			got := Classify(p)
			assert.Equal(t, SourceBrew, got, "brew prefix path %s should classify as brew", p)
		})
	}
}

func TestClassify_ManualPaths(t *testing.T) {
	cases := []string{
		"/Users/michael/go/bin/devm",
		"/usr/local/bin/devm",
		"/tmp/devm-test",
		"/home/agent/.local/bin/devm",
		"./devm",
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			got := Classify(p)
			assert.Equal(t, SourceManual, got, "non-brew path %s should classify as manual", p)
		})
	}
}

func TestClassify_EmptyPathIsManual(t *testing.T) {
	assert.Equal(t, SourceManual, Classify(""))
}

func TestSourceString(t *testing.T) {
	assert.Equal(t, "brew", SourceBrew.String())
	assert.Equal(t, "manual", SourceManual.String())
}
