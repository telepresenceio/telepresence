package cli

import (
	"context"

	"github.com/spf13/cobra"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
)

func helmCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "helm",
	}
	cmd.AddCommand(installCommand(), uninstallCommand())
	return cmd
}

type installArgs struct {
}

func installCommand() *cobra.Command {
	ia := &installArgs{}
	cmd := &cobra.Command{
		Use:  "install",
		Args: cobra.NoArgs,

		Short: "Install telepresence traffic manager",
		RunE:  ia.runInstall,
	}

	return cmd
}

func (ia *installArgs) runInstall(cmd *cobra.Command, args []string) error {
	//kubeFlags := pflag.NewFlagSet("Kubernetes flags", 0)
	request := &connector.ConnectRequest{
		//KubeFlags: kubeFlagMap(kubeFlags),
		EnsureTrafficManager: true,
	}
	return withConnector(cmd, false, request, func(ctx context.Context, cs *connectorState) error {
		return nil
	})
}
