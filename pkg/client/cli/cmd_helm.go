package cli

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
)

func helmCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "helm",
	}
	cmd.AddCommand(installCommand(), uninstallCommand())
	return cmd
}

type installArgs struct {
	replace bool
	values  []string
}

func installCommand() *cobra.Command {
	ia := &installArgs{}
	cmd := &cobra.Command{
		Use:  "install",
		Args: cobra.NoArgs,

		Short: "Install telepresence traffic manager",
		RunE:  ia.runInstall,
	}

	flags := cmd.Flags()
	flags.BoolVarP(&ia.replace, "replace", "r", false, "replace the traffic mangaer if it already exists")
	flags.StringSliceVarP(&ia.values, "values", "f", []string{}, "specify values in a YAML file or a URL (can specify multiple)")

	// copied from connect cmd
	// kubeConfig := genericclioptions.NewConfigFlags(false)
	// kubeConfig.Namespace = nil
	// kubeConfig.AddFlags(kubeFlags)
	flags.AddFlagSet(kubeFlags)

	return cmd
}

func (ia *installArgs) runInstall(cmd *cobra.Command, args []string) error {
	for i, path := range ia.values {
		absPath, err := filepath.Abs(path)
		if err != nil {
			return fmt.Errorf("--values path %q not valid: %w", path, err)
		}
		ia.values[i] = absPath
	}

	request := &connector.ConnectRequest{
		KubeFlags: kubeFlagMap(kubeFlags),
		EnsureManager: &connector.HelmInfo{
			Replace:    ia.replace,
			ValuePaths: ia.values,
		},
	}

	// if the traffic manager should be replaced, quit first so ensureManager is called
	if ia.replace {
		err := cliutil.Disconnect(cmd.Context(), false, false)
		if err != nil {
			dlog.Debugf(cmd.Context(), "Dry run quit error: %v", err)
		}
	}

	return withConnector(cmd, true, request, func(ctx context.Context, cs *connectorState) error {
		// connect with EnsureManager set will install the traffic agent
		return nil
	})
}
