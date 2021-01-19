package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/datawire/telepresence2/pkg/rpc/manager"
)

func previewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "preview",
		Short: "Create or remove preview domains for existing intercepts",
		Args:  OnlySubcommands,
		RunE:  RunSubcommands,
	}

	createCmd := &cobra.Command{
		Use:   "create INTERCEPT_NAME",
		Short: "Create a preview domain for an existing intercept",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			si := &sessionInfo{cmd: cmd}
			return si.withConnector(true, func(cs *connectorState) error {
				ingress, err := cs.selectIngress(cmd.InOrStdin(), cmd.OutOrStdout())
				if err != nil {
					return err
				}
				intercept, err := cs.managerClient.UpdateIntercept(cmd.Context(), &manager.UpdateInterceptRequest{
					Session: cs.info.SessionInfo,
					Name:    args[0],
					PreviewDomainAction: &manager.UpdateInterceptRequest_AddPreviewDomain{
						AddPreviewDomain: &manager.PreviewSpec{
							Ingress:       ingress,
							DisplayBanner: true, // FIXME(lukeshu): Don't hard-code this
						},
					},
				})
				if err != nil {
					return err
				}
				fmt.Println(DescribeIntercept(intercept, false))
				return nil
			})
		},
	}

	removeCmd := &cobra.Command{
		Use:   "remove INTERCEPT_NAME",
		Short: "Remove a preview domain from an intercept",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			si := &sessionInfo{cmd: cmd}
			return si.withConnector(true, func(cs *connectorState) error {
				intercept, err := cs.managerClient.UpdateIntercept(cmd.Context(), &manager.UpdateInterceptRequest{
					Session: cs.info.SessionInfo,
					Name:    args[0],
					PreviewDomainAction: &manager.UpdateInterceptRequest_RemovePreviewDomain{
						RemovePreviewDomain: true,
					},
				})
				if err != nil {
					return err
				}
				fmt.Println(DescribeIntercept(intercept, false))
				return nil
			})
		},
	}

	cmd.AddCommand(createCmd, removeCmd)

	return cmd
}
