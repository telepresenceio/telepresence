package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
)

type listContextsCommand struct {
	rq *daemon.Request
}

func listContexts() *cobra.Command {
	lcc := &listContextsCommand{}

	cmd := &cobra.Command{
		Use:   "list-contexts",
		Args:  cobra.NoArgs,
		Short: "Show all contexts",
		RunE:  lcc.run,
	}
	lcc.rq = daemon.InitRequest(cmd)
	return cmd
}

func (lcc *listContextsCommand) run(cmd *cobra.Command, _ []string) error {
	config, err := lcc.rq.GetConfig(cmd)
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	kcmap := config.Contexts

	if output.WantsFormatted(cmd) {
		output.Object(ctx, kcmap, false)
	} else {
		for name, kc := range kcmap {
			fmt.Fprintf(output.Out(ctx), "- name: %s\n  default namespace: %s\n", name, kc.Namespace)
		}
	}
	return nil
}
