package skills

import "embed"

// Embedded markdown files. Add new skills/references by creating a
// *.md file in this directory; the go:embed pattern below picks
// them up automatically. Each file must start with YAML frontmatter
// (name, description, hidden).
//
//go:embed *.md
var content embed.FS
