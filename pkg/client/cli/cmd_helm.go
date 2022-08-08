package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
)

func helmCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "helm",
	}
	cmd.AddCommand(installCommand(), uninstallCommand())
	return cmd
}

type installArgs struct {
	upgrade          bool
	values           []string
	kubeFlags        *pflag.FlagSet
	dnsIP            string
	mappedNamespaces []string
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
	flags.BoolVarP(&ia.upgrade, "upgrade", "u", false, "replace the traffic manager if it already exists")
	flags.StringSliceVarP(&ia.values, "values", "f", []string{}, "specify values in a YAML file or a URL (can specify multiple)")

	// copied from connect cmd
	kubeFlags := pflag.NewFlagSet("Kubernetes flags", 0)
	kubeConfig := genericclioptions.NewConfigFlags(false)
	kubeConfig.Namespace = nil
	kubeConfig.AddFlags(kubeFlags)
	flags.AddFlagSet(kubeFlags)
	ia.kubeFlags = kubeFlags

	// copied from connect cmd
	nwFlags := pflag.NewFlagSet("Telepresence networking flags", 0)
	// TODO: Those flags aren't applicable on a Linux with systemd-resolved configured either but
	//  that's unknown until it's been tested during the first connect attempt.
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		nwFlags.StringVarP(&ia.dnsIP,
			"dns", "", "",
			"DNS IP address to intercept locally. Defaults to the first nameserver listed in /etc/resolv.conf.",
		)
	}
	nwFlags.StringSliceVar(&ia.mappedNamespaces,
		"mapped-namespaces", nil, ``+
			`Comma separated list of namespaces considered by DNS resolver and NAT for outbound connections. `+
			`Defaults to all namespaces`)
	flags.AddFlagSet(nwFlags)

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

	request := &connector.InstallRequest{
		Upgrade:    ia.upgrade,
		ValuePaths: ia.values,

		ConnectRequest: &connector.ConnectRequest{
			KubeFlags:        kubeFlagMap(ia.kubeFlags),
			MappedNamespaces: ia.mappedNamespaces,
		},
	}
	addKubeconfigEnv(request.ConnectRequest)

	// if the traffic manager should be replaced, quit first
	// so the roodD doesnt hang
	if ia.upgrade {
		err := cliutil.Disconnect(cmd.Context(), false, false)
		if err != nil {
			dlog.Debugf(cmd.Context(), "Dry run quit error: %v", err)
		}
	}

	return cliutil.WithNetwork(cmd.Context(), func(ctx context.Context, daemonClient daemon.DaemonClient) error {
		return cliutil.WithConnector(ctx, func(ctx context.Context, connectorClient connector.ConnectorClient) error {
			resp, err := connectorClient.Install(ctx, request)
			if err != nil {
				return err
			}
			if resp.ErrorText != "" {
				ec := errcat.Unknown
				if resp.ErrorCategory != 0 {
					ec = errcat.Category(resp.ErrorCategory)
				}
				return ec.New(resp.ErrorText)
			}

			fmt.Fprint(cmd.OutOrStdout(), "\nTraffic Manager installed successfully\n")
			return nil
		})
	})
}
