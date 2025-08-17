package cmd

import (
	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/docker/compose"
)

func composeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:              "compose command [flags]",
		Short:            "Define and run multi-container applications with Telepresence and Docker",
		TraverseChildren: true,
	}
	cmd.AddCommand(compose.GenerateSubCommands(cmd)...)
	return cmd
}
