package schema

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// RepoCommand is one entry under `repos.<name>.commands`. Exec is
// required. Startup=nil means "not a startup command" — the pointer
// keeps parity with every other optional bool in the schema (see repo
// CLAUDE.md's nullable-pointer rule).
type RepoCommand struct {
	Exec    string `yaml:"exec"`
	Startup *bool  `yaml:"startup,omitempty"`
}

var commandKnownFields = []string{"exec", "startup"}

func (c *RepoCommand) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("command must be a mapping (line %d)", node.Line)
	}
	known := make(map[string]bool, len(commandKnownFields))
	for _, k := range commandKnownFields {
		known[k] = true
	}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if !known[key] {
			return fmt.Errorf(
				"unknown field %q at command (line %d) — valid: %s",
				key, node.Content[i].Line,
				strings.Join(commandKnownFields, ", "))
		}
	}
	type raw struct {
		Exec    string `yaml:"exec"`
		Startup *bool  `yaml:"startup,omitempty"`
	}
	var r raw
	if err := node.Decode(&r); err != nil {
		return err
	}
	c.Exec = r.Exec
	c.Startup = r.Startup
	return nil
}

// Validate checks one command's exec is present and shell-parseable.
func (c RepoCommand) Validate() error {
	if strings.TrimSpace(c.Exec) == "" {
		return fmt.Errorf("exec is required")
	}
	return ValidateShellCommand(c.Exec)
}

// StartupBool returns c.Startup dereferenced, false when nil.
func (c RepoCommand) StartupBool() bool { return c.Startup != nil && *c.Startup }

var commandNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// validateCommands validates every command in this repo's Commands map
// against the command-name shape rule and its own Validate. Called from
// RepoConfig.Validate. Iteration is sorted so a config with multiple
// errors surfaces them deterministically.
func (r RepoConfig) validateCommands() error {
	if len(r.Commands) == 0 {
		return nil
	}
	names := make([]string, 0, len(r.Commands))
	for name := range r.Commands {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !commandNameRE.MatchString(name) {
			return fmt.Errorf("command name %q: must match /^[a-z][a-z0-9_-]*$/", name)
		}
		if err := r.Commands[name].Validate(); err != nil {
			return fmt.Errorf("command %q: %w", name, err)
		}
	}
	return nil
}
