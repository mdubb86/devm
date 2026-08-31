package schema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateShellCommand_Ok(t *testing.T) {
	for _, s := range []string{
		`echo hello`,
		`echo "hello world"`,
		`echo 'hello world'`,
		`grep -r "pattern" .`,
		`git commit -m "msg with $var and # inside quotes"`,
		`export FOO=1; export BAR=2`,
		`echo hello # trailing bash comment`, // bash comment is fine — full closer, `#` at word boundary
		`printf '%s\n' foo`,
	} {
		t.Run(s, func(t *testing.T) {
			assert.NoError(t, ValidateShellCommand(s))
		})
	}
}

// Regression: a word-splitter (github.com/mattn/go-shellwords, the
// original implementation) stops at the first command separator (||,
// &&, ;, |, &) and returns the LHS with no error — silently missing
// an unterminated quote on the RHS of a chain, which is the exact
// shape a YAML `#`-strip leaves behind (`grep -q foo || echo "hello`).
// Fixed by parsing the whole string with mvdan.cc/sh/v3/syntax.
func TestValidateShellCommand_RejectsUnterminatedQuoteAfterOperator(t *testing.T) {
	for _, s := range []string{
		`grep -q 'devm-trust' /etc/f || echo "host all trust`,
		`good && bad "unterminated`,
		`good ; bad "unterminated`,
		`good | bad "unterminated`,
		`good & bad "unterminated`,
	} {
		t.Run(s, func(t *testing.T) {
			err := ValidateShellCommand(s)
			require.Error(t, err)
			assert.Contains(t, err.Error(), `shell parse failed`)
		})
	}
}

// The mirror: chains WITHOUT an unterminated quote should still pass.
func TestValidateShellCommand_AllowsWellFormedChains(t *testing.T) {
	for _, s := range []string{
		`grep -q 'devm-trust' /etc/f || echo "host all trust"`,
		`good && bad "closed"`,
		`good ; bad "closed"`,
		`good | bad "closed"`,
		`good && bad || fallback ; final`,
	} {
		t.Run(s, func(t *testing.T) {
			assert.NoError(t, ValidateShellCommand(s))
		})
	}
}

func TestValidateShellCommand_RejectsYamlCommentTruncation(t *testing.T) {
	// The exact shelfmates fixture: author wrote `echo "hello # world"`
	// in an unquoted YAML scalar. YAML stripped ` # world"` as a
	// comment, leaving `echo "hello` — an unterminated double quote.
	err := ValidateShellCommand(`echo "hello`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `shell parse failed`)
	assert.Contains(t, err.Error(), `YAML comment`)
	assert.Contains(t, err.Error(), `single quotes`)
}

func TestValidateShellCommand_RejectsUnterminatedSingleQuote(t *testing.T) {
	err := ValidateShellCommand(`echo 'hello`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `shell parse failed`)
}

func TestValidate_InstallCatchesYamlCommentTruncation(t *testing.T) {
	c := Config{
		Project: Project{Name: "p"},
		Install: []string{`echo "hello`},
	}
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `install[0]`)
	assert.Contains(t, err.Error(), `YAML comment`)
}

func TestValidate_StartupCatchesYamlCommentTruncation(t *testing.T) {
	c := Config{
		Project: Project{Name: "p"},
		Startup: []string{`echo 'unterminated`},
	}
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `startup[0]`)
}

func TestValidate_ScriptsCatchesYamlCommentTruncation(t *testing.T) {
	c := Config{
		Project: Project{Name: "p"},
		Scripts: map[string][]string{
			"seed": {`export FOO="bar`},
		},
	}
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `scripts[seed][0]`)
}

func TestValidate_InstallScriptRefsSkipShellLint(t *testing.T) {
	// `>name` is a script ref, expanded at render time — not a literal
	// shell command. Must not be shell-linted (a lone `>` would look
	// odd to a tokenizer regardless).
	c := Config{
		Project: Project{Name: "p"},
		Scripts: map[string][]string{"seed": {`echo ok`}},
		Install: []string{`>seed`},
	}
	assert.NoError(t, c.Validate())
}
