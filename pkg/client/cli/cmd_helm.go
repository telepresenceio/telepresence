package cli

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
)

func helmCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "helm",
	}
	cmd.AddCommand(helmInstallCommand(), helmUninstallCommand())
	return cmd
}

type helmArgs struct {
	cmdType   connector.HelmRequest_Type
	values    []string
	request   *connector.ConnectRequest
	kubeFlags *pflag.FlagSet
}

func helmInstallCommand() *cobra.Command {
	var upgrade bool
	ha := &helmArgs{
		cmdType: connector.HelmRequest_INSTALL,
	}
	cmd := &cobra.Command{
		Use:   "install",
		Args:  cobra.NoArgs,
		Short: "Install telepresence traffic manager",
		RunE: func(cmd *cobra.Command, args []string) error {
			if upgrade {
				ha.cmdType = connector.HelmRequest_UPGRADE
			}
			return ha.run(cmd, args)
		},
	}

	flags := cmd.Flags()
	flags.BoolVarP(&upgrade, "upgrade", "u", false, "replace the traffic manager if it already exists")
	flags.StringSliceVarP(&ha.values, "values", "f", []string{}, "specify values in a YAML file or a URL (can specify multiple)")

	ha.request, ha.kubeFlags = initConnectRequest(cmd)
	return cmd
}

func helmUninstallCommand() *cobra.Command {
	ha := &helmArgs{
		cmdType: connector.HelmRequest_UNINSTALL,
	}
	cmd := &cobra.Command{
		Use:   "uninstall",
		Args:  cobra.NoArgs,
		Short: "Uninstall telepresence traffic manager",
		RunE:  ha.run,
	}
	ha.request, ha.kubeFlags = initConnectRequest(cmd)
	return cmd
}

func (ha *helmArgs) run(cmd *cobra.Command, args []string) error {
	ha.request.KubeFlags = kubeFlagMap(ha.kubeFlags)
	for i, path := range ha.values {
		absPath, err := filepath.Abs(path)
		if err != nil {
			return fmt.Errorf("--values path %q not valid: %w", path, err)
		}
		ha.values[i] = absPath
	}
	request := &connector.HelmRequest{
		Type:           ha.cmdType,
		ValuePaths:     ha.values,
		ConnectRequest: ha.request,
	}
	addKubeconfigEnv(request.ConnectRequest)

	// always disconnect to ensure that there are no running intercepts etc.
	ctx := cmd.Context()
	_ = cliutil.Disconnect(ctx, false)

	doQuit := false
	err := cliutil.WithConnector(ctx, func(ctx context.Context, connectorClient connector.ConnectorClient) error {
		status, _ := connectorClient.Status(ctx, &empty.Empty{})
		resp, err := connectorClient.Helm(ctx, request)
		if err != nil {
			return err
		}
		if err = errcat.FromResult(resp); err != nil {
			return err
		}

		var msg string
		switch ha.cmdType {
		case connector.HelmRequest_INSTALL:
			msg = "installed"
		case connector.HelmRequest_UPGRADE:
			msg = "upgraded"
		case connector.HelmRequest_UNINSTALL:
			if status != nil {
				if err = removeClusterFromUserCache(ctx, status); err != nil {
					return err
				}
			}
			doQuit = true
			msg = "uninstalled"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "\nTraffic Manager %s successfully\n", msg)
		return nil
	})
	if err == nil && doQuit {
		err = cliutil.Disconnect(cmd.Context(), true)
	}
	return err
}

func removeClusterFromUserCache(ctx context.Context, connInfo *connector.ConnectInfo) (err error) {
	// Login token is affined to the traffic-manager that just got removed. The user-info
	// in turn, is info obtained using that token so both are removed here as a
	// consequence of removing the manager.
	if err := cliutil.EnsureLoggedOut(ctx); err != nil {
		return err
	}

	// Delete the ingress info for the cluster if it exists.
	ingresses, err := cache.LoadIngressesFromUserCache(ctx)
	if err != nil {
		return err
	}

	key := connInfo.ClusterServer + "/" + connInfo.ClusterContext
	if _, ok := ingresses[key]; ok {
		delete(ingresses, key)
		if err = cache.SaveIngressesToUserCache(ctx, ingresses); err != nil {
			return err
		}
	}
	return nil
}
