package cmd

import (
	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/intercept"
)

func interceptCmd() *cobra.Command {
	ic := &intercept.Command{}
	cmd := &cobra.Command{
		Use:   "intercept [flags] <intercept_base_name> [-- <command with arguments...>]",
		Args:  cobra.MinimumNArgs(1),
		Short: "Intercept a service",
		Annotations: map[string]string{
			ann.Session:           ann.Required,
			ann.UpdateCheckFormat: ann.Tel2,
		},
		SilenceUsage:      true,
		SilenceErrors:     true,
		RunE:              ic.Run,
		ValidArgsFunction: ic.ValidArgs,
	}
	ic.AddFlags(cmd)
	return cmd
}
