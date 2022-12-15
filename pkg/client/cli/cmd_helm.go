package cli

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
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
	mode       manager.Mode
}

func helmInstallCommand() *cobra.Command {
	var (
		upgrade              bool
		teamMode, singleMode bool
	)

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
			switch {
			case teamMode && singleMode:
				return fmt.Errorf("flags `--team-mode` and `--single-mode` are mutually exclusive")
			case teamMode:
				ha.mode = manager.Mode_MODE_TEAM
			case singleMode:
				ha.mode = manager.Mode_MODE_SINGLE
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
	flags.BoolVarP(&teamMode, "team-mode", "", false, "set the traffic-manager to team mode")
	flags.BoolVarP(&singleMode, "single-mode", "", false, "set the traffic-manager to single user mode")

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

	// always disconnect to ensure that there are no running intercepts etc.
	ctx := cmd.Context()
	_ = util.Disconnect(ctx, false)

	doQuit := false
	userD := util.GetUserDaemon(ctx)

	request := &connector.HelmRequest{
		Type:           ha.cmdType,
		ValuePaths:     ha.values,
		ValuePairs:     ha.valuePairs,
		ConnectRequest: ha.request,
	}

	if ha.mode != manager.Mode_MODE_UNSPECIFIED {
		switch ha.mode {
		case manager.Mode_MODE_SINGLE:
			request.ValuePairs = append(request.ValuePairs, "trafficManager.mode=single")
		case manager.Mode_MODE_TEAM:
			request.ValuePairs = append(request.ValuePairs, "trafficManager.mode=team")
		}

		upgrade := ha.cmdType == connector.HelmRequest_UPGRADE
		setFlagUsed := 0 < len(ha.valuePairs)
		valuesFlagUsed := 0 < len(ha.values)
		if upgrade && !setFlagUsed && !valuesFlagUsed {
			request.ReuseValues = true
		}
	}

	util.AddKubeconfigEnv(request.ConnectRequest)
	resp, err := userD.Helm(ctx, request)
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
