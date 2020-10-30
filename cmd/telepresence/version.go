package main

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// AddVersionCommand adds the version sub-command.
func AddVersionCommand(topLevel *cobra.Command) {
	topLevel.AddCommand(&cobra.Command{
		Use:   "version",
		Short: `Print version of this executable.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(topLevel.OutOrStdout(), Version())
			return nil
		}})
}

var version string

func Version() string {
	if version == "" {
		if i, ok := debug.ReadBuildInfo(); ok {
			version = i.Main.Version
		} else {
			version = "(unknown version)"
		}
	}
	return version
}
