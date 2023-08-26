package cmd

import (
	"encoding/json"
	"path/filepath"

	"github.com/spf13/cobra"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

func config() *cobra.Command {
	cmd := &cobra.Command{
		Use: "config",
	}
	cmd.AddCommand(configView())
	return cmd
}

func configView() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "view",
		Args:              cobra.NoArgs,
		PersistentPreRunE: output.DefaultYAML,
		Short:             "View current Telepresence configuration",
		RunE:              runConfigView,
		Annotations: map[string]string{
			ann.Session: ann.Optional,
		},
	}
	cmd.Flags().BoolP("client-only", "c", false, "Only view config from client file.")
	return cmd
}

func runConfigView(cmd *cobra.Command, _ []string) error {
	var cfg client.SessionConfig
	clientOnly, _ := cmd.Flags().GetBool("client-only")
	if clientOnly {
		// Unable to establish a session, so try to convey the local config instead. It
		// may be helpful in diagnosing the problem.
		if err := connect.InitCommand(cmd); err != nil {
			return err
		}

		ctx := cmd.Context()
		uc := daemon.GetUserClient(ctx)
		cfg.Config = client.GetConfig(ctx)
		cfg.ClientFile = filepath.Join(filelocation.AppUserConfigDir(ctx), client.ConfigFile)
		var kc *client.Kubeconfig
		var err error
		if uc != nil && !cmd.Flag("context").Changed {
			// Get the context that we're currently connected to.
			var ci *connector.ConnectInfo
			ci, err = uc.Status(ctx, &empty.Empty{})
			if err == nil {
				kc, err = client.NewKubeconfig(ctx, map[string]string{"context": ci.ClusterContext}, "")
			}
		} else {
			rq := daemon.GetRequest(daemon.WithDefaultRequest(ctx, cmd))
			kc, err = client.NewKubeconfig(ctx, rq.KubeFlags, rq.ManagerNamespace)
		}
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
	if err := connect.InitCommand(cmd); err != nil {
		return err
	}
	ctx := cmd.Context()
	cc, err := daemon.GetUserClient(ctx).GetConfig(ctx, &empty.Empty{})
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
