package cli

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/util"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

func helmCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "helm",
	}
	cmd.AddCommand(helmInstallCommand(), helmUninstallCommand())
	return cmd
}

type helmArgs struct {
	cmdType    connector.HelmRequest_Type
	values     []string
	valuePairs []string
	request    *connector.ConnectRequest
	kubeFlags  *pflag.FlagSet
}

var (
	HelmInstallExtendFlagsFunc func(*pflag.FlagSet)                                                //nolint:gochecknoglobals // extension point
	HelmInstallPrologFunc      func(context.Context, *pflag.FlagSet, *connector.HelmRequest) error //nolint:gochecknoglobals // extension point
)

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
		Annotations: map[string]string{
			ann.UserDaemon: ann.Required,
		},
	}

	flags := cmd.Flags()
	flags.BoolVarP(&upgrade, "upgrade", "u", false, "replace the traffic manager if it already exists")
	flags.StringSliceVarP(&ha.values, "values", "f", []string{}, "specify values in a YAML file or a URL (can specify multiple)")
	flags.StringSliceVarP(&ha.valuePairs, "set", "", []string{}, "specify a value as a.b=v (can specify multiple or separate values with commas: a.b=v1,a.c=v2)")
	if HelmInstallExtendFlagsFunc != nil {
		HelmInstallExtendFlagsFunc(flags)
	}

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
		Annotations: map[string]string{
			ann.UserDaemon: ann.Required,
		},
	}
	ha.request, ha.kubeFlags = initConnectRequest(cmd)
	return cmd
}

func (ha *helmArgs) run(cmd *cobra.Command, _ []string) error {
	if err := util.InitCommand(cmd); err != nil {
		return err
	}
	ha.request.KubeFlags = kubeFlagMap(ha.kubeFlags)
	for i, path := range ha.values {
		absPath, err := filepath.Abs(path)
		if err != nil {
			return fmt.Errorf("--values path %q not valid: %w", path, err)
		}
		ha.values[i] = absPath
	}

	util.AddKubeconfigEnv(ha.request)
	request := &connector.HelmRequest{
		Type:           ha.cmdType,
		ValuePaths:     ha.values,
		ValuePairs:     ha.valuePairs,
		ConnectRequest: ha.request,
	}

	ctx := cmd.Context()
	if HelmInstallPrologFunc != nil {
		if err := HelmInstallPrologFunc(ctx, cmd.Flags(), request); err != nil {
			return err
		}
	}

	// always disconnect to ensure that there are no running intercepts etc.
	_ = util.Disconnect(ctx, false)

	doQuit := false
	resp, err := util.GetUserDaemon(ctx).Helm(ctx, request)
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
		doQuit = true
		msg = "uninstalled"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "\nTraffic Manager %s successfully\n", msg)
	if err == nil && doQuit {
		err = util.Disconnect(cmd.Context(), true)
	}
	return err
}
