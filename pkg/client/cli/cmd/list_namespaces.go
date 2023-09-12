package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
)

type listNamespacesCommand struct {
	rq *daemon.Request
}

func listNamespaces() *cobra.Command {
	lnc := &listNamespacesCommand{}

	cmd := &cobra.Command{
		Use:   "list-namespaces",
		Args:  cobra.NoArgs,
		Short: "Show all namespaces",
		RunE:  lnc.run,
	}
	lnc.rq = daemon.InitRequest(cmd)
	return cmd
}

func (lnc *listNamespacesCommand) run(cmd *cobra.Command, _ []string) error {
	nss, err := lnc.rq.GetAllNamespaces(cmd)
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	if output.WantsFormatted(cmd) {
		output.Object(ctx, nss, false)
	} else {
		for _, ns := range nss {
			fmt.Fprintln(output.Out(ctx), ns)
		}
	}
	return nil
}
