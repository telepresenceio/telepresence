package client

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/spf13/cobra"

	"github.com/datawire/telepresence2/pkg/version"
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

func Version() string {
	// Prefer version number inserted at build
	if version.Version != "" {
		return version.Version
	}

	v := os.Getenv("TELEPRESENCE_VERSION")
	if v != "" {
		version.Version = v
		return v
	}

	// Fall back to version info from "go get"
	if i, ok := debug.ReadBuildInfo(); ok {
		version.Version = i.Main.Version
		return version.Version
	}
	return "(unknown version)"
}
