// Package skills exposes embedded markdown — the active workflow
// skill plus reference cheatsheets — via List() and Get(name).
// Used by `devm skills` to serve content to the Claude Code agent.
package skills

import (
	"bufio"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Skill is one entry parsed from an embedded *.md file.
type Skill struct {
	Name        string
	Description string
	Hidden      bool
	Body        string
}

// List returns every embedded skill sorted by name.
func List() ([]Skill, error) {
	entries, err := fs.ReadDir(content, ".")
	if err != nil {
		return nil, fmt.Errorf("skills: read embed root: %w", err)
	}
	out := make([]Skill, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		raw, err := fs.ReadFile(content, e.Name())
		if err != nil {
			return nil, fmt.Errorf("skills: read %s: %w", e.Name(), err)
		}
		s, err := parseSkill(e.Name(), string(raw))
		if err != nil {
			return nil, fmt.Errorf("skills: parse %s: %w", e.Name(), err)
		}
		out = append(out, s)
	}
	// Sort alphabetically by name for stable list output.
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get returns the skill with the given name, or an error if not found.
func Get(name string) (Skill, error) {
	all, err := List()
	if err != nil {
		return Skill{}, err
	}
	for _, s := range all {
		if s.Name == name {
			return s, nil
		}
	}
	return Skill{}, fmt.Errorf("skills: unknown skill %q", name)
}

// parseSkill splits a markdown file into frontmatter + body.
// Expected shape:
//
//	---
//	name: <slug>
//	description: <one-liner>
//	hidden: false
//	---
//
//	# Body markdown follows.
func parseSkill(filename, raw string) (Skill, error) {
	sc := bufio.NewScanner(strings.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	if !sc.Scan() || strings.TrimSpace(sc.Text()) != "---" {
		return Skill{}, fmt.Errorf("missing frontmatter opener (file %s)", filename)
	}

	var frontmatter strings.Builder
	closed := false
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "---" {
			closed = true
			break
		}
		frontmatter.WriteString(line)
		frontmatter.WriteByte('\n')
	}
	if !closed {
		return Skill{}, fmt.Errorf("missing frontmatter closer (file %s)", filename)
	}

	var body strings.Builder
	for sc.Scan() {
		body.WriteString(sc.Text())
		body.WriteByte('\n')
	}
	if err := sc.Err(); err != nil {
		return Skill{}, fmt.Errorf("scan %s: %w", filename, err)
	}

	var meta struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
		Hidden      bool   `yaml:"hidden"`
	}
	if err := yaml.Unmarshal([]byte(frontmatter.String()), &meta); err != nil {
		return Skill{}, fmt.Errorf("yaml parse %s: %w", filename, err)
	}
	if meta.Name == "" {
		return Skill{}, fmt.Errorf("missing required field 'name' in %s", filename)
	}
	return Skill{
		Name:        meta.Name,
		Description: meta.Description,
		Hidden:      meta.Hidden,
		Body:        strings.TrimLeft(body.String(), "\n"),
	}, nil
}
