package cli

import (
	"context"
	"io"

	"github.com/spf13/cobra"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/config"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/util"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

type StatusInfo struct {
	RootDaemon io.WriterTo `json:"root_daemon" yaml:"root_daemon"`
	UserDaemon io.WriterTo `json:"user_daemon" yaml:"user_daemon"`
}

type rootDaemonStatus struct {
	Running              bool             `json:"running,omitempty" yaml:"running,omitempty"`
	Version              string           `json:"version,omitempty" yaml:"version,omitempty"`
	APIVersion           int32            `json:"api_version,omitempty" yaml:"api_version,omitempty"`
	DNS                  *client.DNSSnake `json:"dns,omitempty" yaml:"dns,omitempty"`
	*client.RoutingSnake `yaml:",inline"`
}

type userDaemonStatus struct {
	Running           bool                     `json:"running,omitempty" yaml:"running,omitempty"`
	Version           string                   `json:"version,omitempty" yaml:"version,omitempty"`
	APIVersion        int32                    `json:"api_version,omitempty" yaml:"api_version,omitempty"`
	Executable        string                   `json:"executable,omitempty" yaml:"executable,omitempty"`
	InstallID         string                   `json:"install_id,omitempty" yaml:"install_id,omitempty"`
	Status            string                   `json:"status,omitempty" yaml:"status,omitempty"`
	Mode              int32                    `json:"mode,omitempty" yaml:"status,omitempty"`
	ClientCount       int32                    `json:"client_count,omitempty" yaml:"status,omitempty"`
	Error             string                   `json:"error,omitempty" yaml:"error,omitempty"`
	KubernetesServer  string                   `json:"kubernetes_server,omitempty" yaml:"kubernetes_server,omitempty"`
	KubernetesContext string                   `json:"kubernetes_context,omitempty" yaml:"kubernetes_context,omitempty"`
	Intercepts        []connectStatusIntercept `json:"intercepts,omitempty" yaml:"intercepts,omitempty"`
}

type connectStatusIntercept struct {
	Name   string `json:"name,omitempty" yaml:"name,omitempty"`
	Client string `json:"client,omitempty" yaml:"client,omitempty"`
}

func statusCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:  "status",
		Args: cobra.NoArgs,

		Short:             "Show connectivity status",
		RunE:              run,
		PersistentPreRunE: fixFlag,
		Annotations: map[string]string{
			ann.RootDaemon: ann.Optional,
			ann.UserDaemon: ann.Optional,
		},
	}
	flags := cmd.Flags()
	flags.BoolP("json", "j", false, "output as json object")
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
func run(cmd *cobra.Command, _ []string) error {
	if err := util.InitCommand(cmd); err != nil {
		return err
	}
	ctx := cmd.Context()

	si, err := GetStatusInfo(ctx)
	if err != nil {
		return err
	}

	if output.WantsFormatted(cmd) {
		output.Object(ctx, si, true)
	} else {
		_, _ = ioutil.WriteAllTo(cmd.OutOrStdout(), si.WriterTos()...)
	}
	return nil
}

// GetStatusInfo may return an extended struct, based on the one returned by the BasicGetStatusInfo.
var GetStatusInfo = BasicGetStatusInfo //nolint:gochecknoglobals // extension point

func BasicGetStatusInfo(ctx context.Context) (ioutil.WriterTos, error) {
	rs := rootDaemonStatus{}
	us := userDaemonStatus{}
	si := &StatusInfo{
		RootDaemon: &rs,
		UserDaemon: &us,
	}
	userD := util.GetUserDaemon(ctx)
	if userD == nil {
		return si, nil
	}
	reporter := scout.NewReporter(ctx, "cli")
	si.UserDaemon = &us
	us.InstallID = reporter.InstallID()
	us.Running = true
	version, err := userD.Version(ctx, &empty.Empty{})
	if err != nil {
		return nil, err
	}
	us.Version = version.Version
	us.APIVersion = version.ApiVersion
	us.Executable = version.Executable

	status, err := userD.Status(ctx, &empty.Empty{})
	if err != nil {
		return nil, err
	}
	switch status.Error {
	case connector.ConnectInfo_UNSPECIFIED, connector.ConnectInfo_ALREADY_CONNECTED:
		us.Status = "Connected"
		us.Mode = status.Status.Mode
		us.ClientCount = status.Status.ClientCount
		us.KubernetesServer = status.ClusterServer
		us.KubernetesContext = status.ClusterContext
		for _, icept := range status.GetIntercepts().GetIntercepts() {
			us.Intercepts = append(us.Intercepts, connectStatusIntercept{
				Name:   icept.Spec.Name,
				Client: icept.Spec.Client,
			})
		}
	case connector.ConnectInfo_MUST_RESTART:
		us.Status = "Connected, but must restart"
	case connector.ConnectInfo_DISCONNECTED:
		us.Status = "Not connected"
	case connector.ConnectInfo_CLUSTER_FAILED:
		us.Status = "Not connected, error talking to cluster"
		us.Error = status.ErrorText
	case connector.ConnectInfo_TRAFFIC_MANAGER_FAILED:
		us.Status = "Not connected, error talking to in-cluster Telepresence traffic-manager"
		us.Error = status.ErrorText
	}

	rStatus := status.DaemonStatus
	if rStatus != nil {
		rs.Running = true
		rs.Version = rStatus.Version.Version
		rs.APIVersion = rStatus.Version.ApiVersion
		if obc := rStatus.OutboundConfig; obc != nil {
			rs.DNS = &client.DNSSnake{}
			dns := obc.Dns
			if dns.LocalIp != nil {
				// Local IP is only set when the overriding resolver is used
				rs.DNS.LocalIP = dns.LocalIp
			}
			rs.DNS.RemoteIP = dns.RemoteIp
			rs.DNS.ExcludeSuffixes = dns.ExcludeSuffixes
			rs.DNS.IncludeSuffixes = dns.IncludeSuffixes
			rs.DNS.LookupTimeout = dns.LookupTimeout.AsDuration()
			rs.RoutingSnake = &client.RoutingSnake{}
			for _, subnet := range obc.AlsoProxySubnets {
				rs.RoutingSnake.AlsoProxy = append(rs.RoutingSnake.AlsoProxy, (*iputil.Subnet)(iputil.IPNetFromRPC(subnet)))
			}
			for _, subnet := range obc.NeverProxySubnets {
				rs.RoutingSnake.NeverProxy = append(rs.RoutingSnake.NeverProxy, (*iputil.Subnet)(iputil.IPNetFromRPC(subnet)))
			}
		}
	}
	return si, nil
}

func (s *StatusInfo) WriterTos() []io.WriterTo {
	return []io.WriterTo{s.UserDaemon, s.RootDaemon}
}

func (ds *rootDaemonStatus) WriteTo(out io.Writer) (int64, error) {
	n := 0
	if ds.Running {
		n += ioutil.Println(out, "Root Daemon: Running")
		n += ioutil.Printf(out, "  Version: %s (api %d)\n", ds.Version, ds.APIVersion)
		if ds.DNS != nil {
			n += printDNS(out, ds.DNS)
		}
		if ds.RoutingSnake != nil {
			n += printRouting(out, ds.RoutingSnake)
		}
	} else {
		n += ioutil.Println(out, "Root Daemon: Not running")
	}
	return int64(n), nil
}

func printDNS(out io.Writer, d *client.DNSSnake) int {
	n := ioutil.Printf(out, "  DNS    :\n")
	if len(d.LocalIP) > 0 {
		n += ioutil.Printf(out, "    Local IP        : %v\n", d.LocalIP)
	}
	n += ioutil.Printf(out, "    Remote IP       : %v\n", d.RemoteIP)
	n += ioutil.Printf(out, "    Exclude suffixes: %v\n", d.ExcludeSuffixes)
	n += ioutil.Printf(out, "    Include suffixes: %v\n", d.IncludeSuffixes)
	n += ioutil.Printf(out, "    Timeout         : %v\n", d.LookupTimeout)
	return n
}

func printRouting(out io.Writer, r *client.RoutingSnake) int {
	n := ioutil.Printf(out, "  Also Proxy : (%d subnets)\n", len(r.AlsoProxy))
	for _, subnet := range r.AlsoProxy {
		n += ioutil.Printf(out, "    - %s\n", subnet)
	}
	n += ioutil.Printf(out, "  Never Proxy: (%d subnets)\n", len(r.NeverProxy))
	for _, subnet := range r.NeverProxy {
		n += ioutil.Printf(out, "    - %s\n", subnet)
	}
	return n
}

func (cs *userDaemonStatus) WriteTo(out io.Writer) (int64, error) {
	n := 0
	if cs.Running {
		n += ioutil.Println(out, "User Daemon: Running")
		n += ioutil.Printf(out, "  Version           : %s (api %d)\n", cs.Version, cs.APIVersion)
		n += ioutil.Printf(out, "  Executable        : %s\n", cs.Executable)
		n += ioutil.Printf(out, "  Install ID        : %s\n", cs.InstallID)
		n += ioutil.Printf(out, "  Status            : %s\n", cs.Status)
		n += ioutil.Printf(out, "    Mode            : %s\n", modeToString(cs.Mode))
		n += ioutil.Printf(out, "    Client Count    : %s\n", cs.ClientCount)
		if cs.Error != "" {
			n += ioutil.Printf(out, "  Error             : %s\n", cs.Error)
		}
		n += ioutil.Printf(out, "  Kubernetes server : %s\n", cs.KubernetesServer)
		n += ioutil.Printf(out, "  Kubernetes context: %s\n", cs.KubernetesContext)
		n += ioutil.Printf(out, "  Intercepts        : %d total\n", len(cs.Intercepts))
		for _, intercept := range cs.Intercepts {
			n += ioutil.Printf(out, "    %s: %s\n", intercept.Name, intercept.Client)
		}
	} else {
		n += ioutil.Println(out, "User Daemon: Not running")
	}
	return int64(n), nil
}

func modeToString(mode int32) string {
	switch mode {
	case int32(config.ModeSingle):
		return "single-user"
	case int32(config.ModeTeam):
		return "team"
	default:
		return "unknown"
	}
}
