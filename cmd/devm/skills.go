package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mdubb86/devm/internal/skills"
)

var skillsCmd = &cobra.Command{
	Use:   "skills",
	Short: "Embedded workflow and reference content for AI agents",
	Long: `Embedded workflow and reference content for AI agents.

The recommended way to wire devm into Claude Code is the skills.sh
meta-installer:

  npx skills add mdubb86/devm -g --agent claude-code

(The 'add' subcommand requires the source argument BEFORE the flags;
putting the flags first errors with 'Missing required argument:
source'.)

That drops two skills under ~/.claude/skills/: a small discovery
stub (devm) and a reference card (using-devm). Claude Code
auto-activates them when working with devm.yaml, then the stub
calls 'devm skills list' / 'devm skills get <name>' to fetch the
workflow content from this binary (so it stays version-locked).

For project-local install drop -g; for other agents swap
--agent claude-code for --agent '*' (or your agent of choice).
Without --agent claude-code the installer drops to
.agents/skills/… instead of .claude/skills/…, and Claude Code
won't see it.

Use the subcommands below if you want to read the embedded content
directly (or to bootstrap installs that don't go through skills.sh).`,
}

var skillsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List embedded skills",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		asJSON, _ := cmd.Flags().GetBool("json")
		all, err := skills.List()
		if err != nil {
			return err
		}
		if asJSON {
			out := make([]map[string]any, 0, len(all))
			for _, s := range all {
				out = append(out, map[string]any{
					"name":        s.Name,
					"description": s.Description,
					"hidden":      s.Hidden,
				})
			}
			return json.NewEncoder(os.Stdout).Encode(out)
		}
		for _, s := range all {
			fmt.Printf("%-12s %s\n", s.Name, s.Description)
		}
		return nil
	},
}

var skillsGetCmd = &cobra.Command{
	Use:   "get <name>",
	Short: "Print the body of an embedded skill (markdown)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		includeFrontmatter, _ := cmd.Flags().GetBool("frontmatter")
		s, err := skills.Get(args[0])
		if err != nil {
			return err
		}
		if includeFrontmatter {
			fmt.Println("---")
			fmt.Printf("name: %s\n", s.Name)
			if s.Description != "" {
				fmt.Printf("description: %s\n", s.Description)
			}
			if s.Hidden {
				fmt.Println("hidden: true")
			}
			fmt.Println("---")
			fmt.Println()
		}
		_, err = fmt.Print(strings.TrimRight(s.Body, "\n") + "\n")
		return err
	},
}

func init() {
	skillsListCmd.Flags().Bool("json", false, "output as JSON")
	skillsGetCmd.Flags().Bool("frontmatter", false, "include the frontmatter block")
	skillsCmd.AddCommand(skillsListCmd)
	skillsCmd.AddCommand(skillsGetCmd)
	rootCmd.AddCommand(skillsCmd)
}
