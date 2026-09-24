package scriptfile

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParse_TopLevelFunctions(t *testing.T) {
	body := []byte(`#!/usr/bin/env bash
set -eo pipefail

install-go-toolchain() {
  echo installing
}

function run-tests {
  make test
}

check-restored() {
  test -f "$WORKSPACE/sentinel"
}
`)
	got, err := Parse(body)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"install-go-toolchain", "run-tests", "check-restored"}, got)
}

func TestParse_IgnoresNestedFunctions(t *testing.T) {
	body := []byte(`
outer() {
  inner() {
    echo nested
  }
  inner
}
`)
	got, err := Parse(body)
	require.NoError(t, err)
	assert.Equal(t, []string{"outer"}, got)
}

func TestParse_IgnoresCommentedFunctions(t *testing.T) {
	body := []byte(`
# real() { would be a function but is commented out }
real-one() { true; }
`)
	got, err := Parse(body)
	require.NoError(t, err)
	assert.Equal(t, []string{"real-one"}, got)
}

func TestValidateFunctionName_AcceptsKebab(t *testing.T) {
	assert.NoError(t, ValidateFunctionName("install-go-toolchain"))
	assert.NoError(t, ValidateFunctionName("a"))
	assert.NoError(t, ValidateFunctionName("a1"))
}

func TestValidateFunctionName_RejectsInvalid(t *testing.T) {
	for _, bad := range []string{"", "_leading-under", "-leading-dash", "UpperCase", "has_underscore", "0starts-with-digit"} {
		assert.Error(t, ValidateFunctionName(bad), "expected reject for %q", bad)
	}
}
