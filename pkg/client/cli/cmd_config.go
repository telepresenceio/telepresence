package cli

import (
	"encoding/json"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/util"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

func configCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "config",
	}
	cmd.AddCommand(configViewCommand())
	return cmd
}

func configViewCommand() *cobra.Command {
	var kubeFlags *pflag.FlagSet
	var request *connector.ConnectRequest

	cmd := &cobra.Command{
		Use:               "view",
		Args:              cobra.NoArgs,
		PersistentPreRunE: output.DefaultYAML,
		Short:             "View current Telepresence configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			request.KubeFlags = FlagMap(kubeFlags)
			util.AddKubeconfigEnv(request)
			cmd.SetContext(util.WithConnectionRequest(cmd.Context(), request))
			return configView(cmd, args)
		},
	}
	cmd.Flags().BoolP("client-only", "c", false, "Only view config from client file.")
	request, kubeFlags = InitConnectRequest(cmd)
	return cmd
}

func configView(cmd *cobra.Command, _ []string) error {
	var cfg client.SessionConfig
	clientOnly, _ := cmd.Flags().GetBool("client-only")
	if clientOnly {
		// Unable to establish a session, so try to convey the local config instead. It
		// may be helpful in diagnosing the problem.
		ctx := cmd.Context()
		cfgDir, err := filelocation.AppUserConfigDir(ctx)
		if err != nil {
			return err
		}
		cfg.Config = client.GetConfig(cmd.Context())
		cfg.ClientFile = filepath.Join(cfgDir, client.ConfigFile)

		rq := util.GetConnectRequest(ctx)
		kc, err := client.NewKubeconfig(ctx, rq.KubeFlags, rq.ManagerNamespace)
		if err != nil {
			return err
		}
		cfg.Routing.AlsoProxy = kc.AlsoProxy
		cfg.Routing.NeverProxy = kc.NeverProxy
		if dns := kc.DNS; dns != nil {
			cfg.DNS.ExcludeSuffixes = dns.ExcludeSuffixes
			cfg.DNS.IncludeSuffixes = dns.IncludeSuffixes
			cfg.DNS.LookupTimeout = dns.LookupTimeout.Duration
			cfg.DNS.LocalIP = dns.LocalIP.IP()
			cfg.DNS.RemoteIP = dns.RemoteIP.IP()
		}
		if mgr := kc.Manager; mgr != nil {
			cfg.ManagerNamespace = mgr.Namespace
		}
		output.Object(ctx, &cfg, true)
		return nil
	}

	cmd.Annotations = map[string]string{
		ann.Session: ann.Required,
	}
	if err := util.InitCommand(cmd); err != nil {
		return err
	}
	ctx := cmd.Context()
	cc, err := util.GetUserDaemon(ctx).GetConfig(ctx, &empty.Empty{})
	if err != nil {
		return err
	}
	err = json.Unmarshal(cc.Json, &cfg)
	if err != nil {
		return err
	}
	output.Object(ctx, &cfg, true)
	return nil
}
