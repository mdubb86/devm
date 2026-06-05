package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var Version = "dev" // set via -ldflags at release time

var rootCmd = &cobra.Command{
	Use:     "devm",
	Short:   "Mac+VM dev sandbox tool",
	Version: Version,
	// SilenceUsage: don't dump --help after a runtime error. Cobra's
	// default behavior treats any RunE error as a usage problem, which
	// is wrong for things like "sbx run exited" — the user already
	// invoked the command correctly; printing help is noise.
	// SilenceErrors: we print the error ourselves in main() so cobra's
	// default "Error: ..." prefix doesn't double up.
	SilenceUsage:  true,
	SilenceErrors: true,
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
