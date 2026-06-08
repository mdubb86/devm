package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print devm version information",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true

		asJSON, _ := cmd.Flags().GetBool("json")
		check, _ := cmd.Flags().GetBool("check")

		var latest string
		if check {
			latest = fetchLatestForCheck(cmd.Context())
			if latest == Version {
				latest = ""
			}
		}

		printVersion(os.Stdout, Version, Commit, Date, latest, asJSON)
		return nil
	},
}

func init() {
	versionCmd.Flags().Bool("json", false, "emit JSON output")
	versionCmd.Flags().Bool("check", false, "check GitHub for a newer release")
	rootCmd.AddCommand(versionCmd)
}

// versionOutput is used for JSON marshalling.
type versionOutput struct {
	Version        string `json:"version"`
	Commit         string `json:"commit"`
	Date           string `json:"date"`
	Latest         string `json:"latest,omitempty"`
	UpgradeCommand string `json:"upgrade_command,omitempty"`
}

// printVersion writes version information to w. latest is non-empty only when
// a newer version was found by --check. asJSON controls the output format.
func printVersion(w io.Writer, version, commit, date, latest string, asJSON bool) {
	if asJSON {
		out := versionOutput{
			Version: version,
			Commit:  commit,
			Date:    date,
		}
		if latest != "" {
			out.Latest = latest
			out.UpgradeCommand = "devm upgrade"
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		return
	}

	fmt.Fprintf(w, "devm %s  (%s, %s)\n", version, commit, date)
	if latest != "" {
		fmt.Fprintf(w, "  newer version %s available — run `devm upgrade`\n", latest)
	}
}
