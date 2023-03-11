package cmd

import (
	"log"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cloud"
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
		PreRunE:           cloud.UpdateCheckIfDue,
		PostRunE:          cloud.RaiseMessage,
	}
	ic.AddFlags(cmd.Flags())
	if err := cmd.RegisterFlagCompletionFunc("namespace", ic.AutocompleteNamespace); err != nil {
		log.Fatal(err)
	}
	return cmd
}
