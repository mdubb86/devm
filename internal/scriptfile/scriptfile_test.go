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

// TestValidateFunctionName_AcceptsUnderscores pins the widened grammar:
// a lowercase-letter start followed by any mix of letters, digits,
// hyphens, and underscores. Covers the snapshot lifecycle hooks
// (backup_<name> / restore_<name>) and the mixed kebab-plus-underscore
// form (backup_my-db).
func TestValidateFunctionName_AcceptsUnderscores(t *testing.T) {
	for _, good := range []string{"backup_pg", "restore_pg", "backup_my-db", "a_b_c", "a1_b2"} {
		assert.NoError(t, ValidateFunctionName(good), "expected accept for %q", good)
	}
}

func TestValidateFunctionName_RejectsInvalid(t *testing.T) {
	for _, bad := range []string{"", "_leading-under", "-leading-dash", "UpperCase", "0starts-with-digit"} {
		assert.Error(t, ValidateFunctionName(bad), "expected reject for %q", bad)
	}
}
