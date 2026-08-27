package schema

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// RepoConfig declares a git repo to hydrate a volume from at cold-start.
// Used at top level (Config.Repo, the primary workspace) and inside a
// Volume (secondary repos under `volumes:`).
type RepoConfig struct {
	// URL is the git clone URL. Nil at top level means "derive from
	// `git remote get-url origin` in Mac cwd." Nil for a secondary is
	// a validation error (secondaries must declare URL explicitly).
	URL *string `yaml:"url,omitempty"`

	// Secret names an entry in the devm secret store. Iron-proxy
	// substitutes __DEVM_SECRET_<name>__ in the Authorization header
	// at clone time. Required at top level. Optional per-volume
	// (inherits from top-level).
	Secret string `yaml:"secret,omitempty"`

	// Label names the mutagen sync session for this repo.
	Label *string `yaml:"label,omitempty"`

	// Volume, when true, backs this repo with a devm-managed volume
	// instead of a plain bind mount.
	Volume *bool `yaml:"volume,omitempty"`

	// Primary marks this repo as the project's primary workspace.
	Primary *bool `yaml:"primary,omitempty"`

	// Ignore lists mutagen sync ignore patterns.
	Ignore []string `yaml:"ignore,omitempty"`
}

var repoKnownFields = []string{"url", "secret", "label", "volume", "primary", "ignore"}

// Volume is a per-project persistent store, optionally hydrated from git.
type Volume struct {
	// Path is the absolute guest mount point. Required.
	Path string
	// Repo, when non-nil, causes devm to `git clone` into the volume's
	// Mac-side storage on the first cold-start where the storage is empty.
	Repo *RepoConfig
}

var volumeKnownFields = []string{"path", "repo"}

// UnmarshalYAML decodes either the scalar shape (bare guest path) or the
// mapping shape (`{path: ..., repo: ...}`). Rejects unknown keys in the
// mapping shape.
func (v *Volume) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		v.Path = node.Value
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("volume must be a string (guest path) or a mapping (line %d)", node.Line)
	}
	known := make(map[string]bool, len(volumeKnownFields))
	for _, k := range volumeKnownFields {
		known[k] = true
	}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if !known[key] {
			return fmt.Errorf(
				"unknown field %q at volume (line %d) — valid: %s",
				key, node.Content[i].Line,
				strings.Join(volumeKnownFields, ", "))
		}
	}
	type raw struct {
		Path string      `yaml:"path"`
		Repo *RepoConfig `yaml:"repo,omitempty"`
	}
	var r raw
	if err := node.Decode(&r); err != nil {
		return err
	}
	v.Path = r.Path
	v.Repo = r.Repo
	return nil
}

// UnmarshalYAML on RepoConfig enforces unknown-key rejection.
func (r *RepoConfig) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("repo must be a mapping (line %d)", node.Line)
	}
	known := make(map[string]bool, len(repoKnownFields))
	for _, k := range repoKnownFields {
		known[k] = true
	}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if !known[key] {
			return fmt.Errorf(
				"unknown field %q at repo (line %d) — valid: %s",
				key, node.Content[i].Line,
				strings.Join(repoKnownFields, ", "))
		}
	}
	type raw struct {
		URL     *string  `yaml:"url,omitempty"`
		Secret  string   `yaml:"secret,omitempty"`
		Label   *string  `yaml:"label,omitempty"`
		Volume  *bool    `yaml:"volume,omitempty"`
		Primary *bool    `yaml:"primary,omitempty"`
		Ignore  []string `yaml:"ignore,omitempty"`
	}
	var raw2 raw
	if err := node.Decode(&raw2); err != nil {
		return err
	}
	r.URL = raw2.URL
	r.Secret = raw2.Secret
	r.Label = raw2.Label
	r.Volume = raw2.Volume
	r.Primary = raw2.Primary
	r.Ignore = raw2.Ignore
	return nil
}
