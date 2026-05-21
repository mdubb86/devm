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
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
