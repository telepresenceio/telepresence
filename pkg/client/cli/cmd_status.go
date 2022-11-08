package cli

import (
	"context"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/spf13/cobra"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/util"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

type statusInfo struct {
	json bool
	out  io.Writer
}

type statusOutput struct {
	DaemonStatus daemonStatus    `json:"root_daemon"`
	UserDaemon   connectorStatus `json:"user_daemon"`
}

type daemonStatus struct {
	Running           bool             `json:"running,omitempty"`
	Version           string           `json:"version,omitempty"`
	APIVersion        int32            `json:"api_version,omitempty"`
	DNS               *daemonStatusDNS `json:"dns,omitempty"`
	AlsoProxySubnets  []string         `json:"also_proxy_subnets,omitempty"`
	NeverProxySubnets []string         `json:"never_proxy_subnets,omitempty"`
}

type daemonStatusDNS struct {
	LocalIP         net.IP        `json:"local_ip,omitempty"`
	RemoteIP        net.IP        `json:"remote_ip,omitempty"`
	ExcludeSuffixes []string      `json:"exclude_suffixes,omitempty"`
	IncludeSuffixes []string      `json:"include_suffixes,omitempty"`
	LookupTimeout   time.Duration `json:"lookup_timeout_in_nanos,omitempty"`
}

type connectorStatus struct {
	Running           bool                     `json:"running,omitempty"`
	Version           string                   `json:"version,omitempty"`
	APIVersion        int32                    `json:"api_version,omitempty"`
	Executable        string                   `json:"executable,omitempty"`
	InstallID         string                   `json:"install_id,omitempty"`
	Status            string                   `json:"status,omitempty"`
	Error             string                   `json:"error,omitempty"`
	KubernetesServer  string                   `json:"kubernetes_server,omitempty"`
	KubernetesContext string                   `json:"kubernetes_context,omitempty"`
	Intercepts        []connectStatusIntercept `json:"intercepts,omitempty"`
}

type connectStatusIntercept struct {
	Name   string `json:"name,omitempty"`
	Client string `json:"client,omitempty"`
}

func statusCommand() *cobra.Command {
	s := &statusInfo{}
	cmd := &cobra.Command{
		Use:  "status",
		Args: cobra.NoArgs,

		Short:             "Show connectivity status",
		RunE:              s.status,
		PersistentPreRunE: fixFlag,
		Annotations: map[string]string{
			ann.RootDaemon: ann.Optional,
			ann.UserDaemon: ann.Optional,
		},
	}
	flags := cmd.Flags()
	flags.BoolVarP(&s.json, "json", "j", false, "output as json object")
	flags.Lookup("json").Hidden = true
	return cmd
}

func fixFlag(cmd *cobra.Command, _ []string) error {
	flags := cmd.Flags()
	json, err := flags.GetBool("json")
	if err != nil {
		return err
	}
	rootCmd := cmd.Parent()
	if json {
		if err = rootCmd.PersistentFlags().Set("output", "json"); err != nil {
			return err
		}
	}
	return rootCmd.PersistentPreRunE(cmd, flags.Args())
}

// status will retrieve connectivity status from the daemon and print it on stdout.
func (s *statusInfo) status(cmd *cobra.Command, _ []string) error {
	if err := util.InitCommand(cmd); err != nil {
		return err
	}
	s.out = cmd.OutOrStdout()
	ctx := cmd.Context()

	ds, cs, err := s.connectorStatus(ctx)
	if err != nil {
		return err
	}

	if output.WantsFormatted(cmd) {
		s.printJSON(ctx, ds, cs)
	} else {
		s.printText(ds, cs)
	}
	return nil
}

func (s *statusInfo) connectorStatus(ctx context.Context) (*daemonStatus, *connectorStatus, error) {
	cs := &connectorStatus{}
	ds := &daemonStatus{}
	userD := util.GetUserDaemon(ctx)
	if userD == nil {
		return ds, cs, nil
	}
	reporter := scout.NewReporter(ctx, "cli")
	cs.InstallID = reporter.InstallID()
	cs.Running = true
	version, err := userD.Version(ctx, &empty.Empty{})
	if err != nil {
		return nil, nil, err
	}
	cs.Version = version.Version
	cs.APIVersion = version.ApiVersion
	cs.Executable = version.Executable

	status, err := userD.Status(ctx, &empty.Empty{})
	if err != nil {
		return nil, nil, err
	}
	switch status.Error {
	case connector.ConnectInfo_UNSPECIFIED, connector.ConnectInfo_ALREADY_CONNECTED:
		cs.Status = "Connected"
		cs.KubernetesServer = status.ClusterServer
		cs.KubernetesContext = status.ClusterContext
		for _, icept := range status.GetIntercepts().GetIntercepts() {
			cs.Intercepts = append(cs.Intercepts, connectStatusIntercept{
				Name:   icept.Spec.Name,
				Client: icept.Spec.Client,
			})
		}
	case connector.ConnectInfo_MUST_RESTART:
		cs.Status = "Connected, but must restart"
	case connector.ConnectInfo_DISCONNECTED:
		cs.Status = "Not connected"
	case connector.ConnectInfo_CLUSTER_FAILED:
		cs.Status = "Not connected, error talking to cluster"
		cs.Error = status.ErrorText
	case connector.ConnectInfo_TRAFFIC_MANAGER_FAILED:
		cs.Status = "Not connected, error talking to in-cluster Telepresence traffic-manager"
		cs.Error = status.ErrorText
	}

	rStatus := status.DaemonStatus
	if rStatus != nil {
		ds.Running = true
		ds.Version = rStatus.Version.Version
		ds.APIVersion = rStatus.Version.ApiVersion
		if obc := rStatus.OutboundConfig; obc != nil {
			ds.DNS = &daemonStatusDNS{}
			dns := obc.Dns
			if dns.LocalIp != nil {
				// Local IP is only set when the overriding resolver is used
				ds.DNS.LocalIP = dns.LocalIp
			}
			ds.DNS.RemoteIP = dns.RemoteIp
			ds.DNS.ExcludeSuffixes = dns.ExcludeSuffixes
			ds.DNS.IncludeSuffixes = dns.IncludeSuffixes
			ds.DNS.LookupTimeout = dns.LookupTimeout.AsDuration()
			for _, subnet := range obc.AlsoProxySubnets {
				ds.AlsoProxySubnets = append(ds.AlsoProxySubnets, iputil.IPNetFromRPC(subnet).String())
			}
			for _, subnet := range obc.NeverProxySubnets {
				ds.NeverProxySubnets = append(ds.NeverProxySubnets, iputil.IPNetFromRPC(subnet).String())
			}
		}
	}
	return ds, cs, nil
}

func (s *statusInfo) printJSON(ctx context.Context, ds *daemonStatus, cs *connectorStatus) {
	output.Object(ctx, statusOutput{
		DaemonStatus: *ds,
		UserDaemon:   *cs,
	}, true)
}

func (s *statusInfo) printText(ds *daemonStatus, cs *connectorStatus) {
	s.printDaemonText(ds)
	s.printConnectorText(cs)
}

func (s *statusInfo) printDaemonText(ds *daemonStatus) {
	if ds.Running {
		s.println("Root Daemon: Running")
		s.printf("  Version   : %s (api %d)\n", ds.Version, ds.APIVersion)
		if ds.DNS != nil {
			s.printf("  DNS       :\n")
			if len(ds.DNS.LocalIP) > 0 {
				s.printf("    Local IP        : %v\n", ds.DNS.LocalIP)
			}
			s.printf("    Remote IP       : %v\n", ds.DNS.RemoteIP)
			s.printf("    Exclude suffixes: %v\n", ds.DNS.ExcludeSuffixes)
			s.printf("    Include suffixes: %v\n", ds.DNS.IncludeSuffixes)
			s.printf("    Timeout         : %v\n", ds.DNS.LookupTimeout)
			s.printf("  Also Proxy : (%d subnets)\n", len(ds.AlsoProxySubnets))
			for _, subnet := range ds.AlsoProxySubnets {
				s.printf("    - %s\n", subnet)
			}
			s.printf("  Never Proxy: (%d subnets)\n", len(ds.NeverProxySubnets))
			for _, subnet := range ds.NeverProxySubnets {
				s.printf("    - %s\n", subnet)
			}
		}
	} else {
		s.println("Root Daemon: Not running")
	}
}

func (s *statusInfo) printConnectorText(cs *connectorStatus) {
	if cs.Running {
		s.println("User Daemon: Running")
		s.printf("  Version         : %s (api %d)\n", cs.Version, cs.APIVersion)
		s.printf("  Executable      : %s\n", cs.Executable)
		s.printf("  Install ID      : %s\n", cs.InstallID)
		s.printf("  Status            : %s\n", cs.Status)
		if cs.Error != "" {
			s.printf("  Error             : %s\n", cs.Error)
		}
		s.printf("  Kubernetes server : %s\n", cs.KubernetesServer)
		s.printf("  Kubernetes context: %s\n", cs.KubernetesContext)
		s.printf("  Intercepts        : %d total\n", len(cs.Intercepts))
		for _, intercept := range cs.Intercepts {
			s.printf("    %s: %s\n", intercept.Name, intercept.Client)
		}
	} else {
		s.println("User Daemon: Not running")
	}
}

func (s *statusInfo) printf(format string, a ...any) {
	_, _ = fmt.Fprintf(s.out, format, a...)
}

func (s *statusInfo) println(a ...any) {
	_, _ = fmt.Fprintln(s.out, a...)
}
