package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/global"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

type StatusInfo struct {
	RootDaemon io.WriterTo `json:"root_daemon" yaml:"root_daemon"`
	UserDaemon io.WriterTo `json:"user_daemon" yaml:"user_daemon"`
}

type StatusInfoEmbedded struct {
	RootDaemon io.WriterTo `json:"root_daemon" yaml:"root_daemon"`
	UserDaemon io.WriterTo `json:"user_daemon" yaml:"user_daemon"`
}

type rootDaemonStatus struct {
	Running              bool             `json:"running,omitempty" yaml:"running,omitempty"`
	Name                 string           `json:"name,omitempty" yaml:"name,omitempty"`
	Version              string           `json:"version,omitempty" yaml:"version,omitempty"`
	APIVersion           int32            `json:"api_version,omitempty" yaml:"api_version,omitempty"`
	DNS                  *client.DNSSnake `json:"dns,omitempty" yaml:"dns,omitempty"`
	*client.RoutingSnake `yaml:",inline"`
}

type userDaemonStatus struct {
	Running           bool                     `json:"running,omitempty" yaml:"running,omitempty"`
	Name              string                   `json:"name,omitempty" yaml:"name,omitempty"`
	Version           string                   `json:"version,omitempty" yaml:"version,omitempty"`
	APIVersion        int32                    `json:"api_version,omitempty" yaml:"api_version,omitempty"`
	Executable        string                   `json:"executable,omitempty" yaml:"executable,omitempty"`
	InstallID         string                   `json:"install_id,omitempty" yaml:"install_id,omitempty"`
	Status            string                   `json:"status,omitempty" yaml:"status,omitempty"`
	Error             string                   `json:"error,omitempty" yaml:"error,omitempty"`
	KubernetesServer  string                   `json:"kubernetes_server,omitempty" yaml:"kubernetes_server,omitempty"`
	KubernetesContext string                   `json:"kubernetes_context,omitempty" yaml:"kubernetes_context,omitempty"`
	ManagerNamespace  string                   `json:"manager_namespace,omitempty" yaml:"manager_namespace,omitempty"`
	MappedNamespaces  []string                 `json:"mapped_namespaces,omitempty" yaml:"mapped_namespaces,omitempty"`
	Intercepts        []connectStatusIntercept `json:"intercepts,omitempty" yaml:"intercepts,omitempty"`
}

type connectStatusIntercept struct {
	Name   string `json:"name,omitempty" yaml:"name,omitempty"`
	Client string `json:"client,omitempty" yaml:"client,omitempty"`
}

func statusCmd() *cobra.Command {
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
		if err = rootCmd.PersistentFlags().Set(global.FlagOutput, "json"); err != nil {
			return err
		}
	}
	return rootCmd.PersistentPreRunE(cmd, flags.Args())
}

// status will retrieve connectivity status from the daemon and print it on stdout.
func run(cmd *cobra.Command, _ []string) error {
	if err := connect.InitCommand(cmd); err != nil {
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
	userD := daemon.GetUserClient(ctx)
	if userD == nil {
		return &StatusInfo{
			RootDaemon: &rs,
			UserDaemon: &us,
		}, nil
	}
	var wt ioutil.WriterTos
	if userD.Remote {
		sie := StatusInfoEmbedded{
			RootDaemon: &rs,
			UserDaemon: &us,
		}
		wt = &sie
	} else {
		si := StatusInfo{
			RootDaemon: &rs,
			UserDaemon: &us,
		}
		wt = &si
	}
	reporter := scout.NewReporter(ctx, "cli")
	us.InstallID = reporter.InstallID()
	us.Running = true
	version, err := userD.Version(ctx, &empty.Empty{})
	if err != nil {
		return nil, err
	}
	us.Name = version.Name
	if us.Name == "" {
		if userD.Remote {
			us.Name = "Daemon"
		} else {
			us.Name = "User Daemon"
		}
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
		us.KubernetesServer = status.ClusterServer
		us.KubernetesContext = status.ClusterContext
		for _, icept := range status.GetIntercepts().GetIntercepts() {
			us.Intercepts = append(us.Intercepts, connectStatusIntercept{
				Name:   icept.Spec.Name,
				Client: icept.Spec.Client,
			})
		}
		us.ManagerNamespace = status.ManagerNamespace
		us.MappedNamespaces = status.MappedNamespaces
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
		rs.Name = rStatus.Version.Name
		if rs.Name == "" {
			rs.Name = "Root Daemon"
		}
		rs.Version = rStatus.Version.Version
		rs.APIVersion = rStatus.Version.ApiVersion
		if obc := rStatus.OutboundConfig; obc != nil {
			rs.DNS = &client.DNSSnake{}
			dns := obc.Dns
			if dns.LocalIp != nil {
				// Local IP is only set when the overriding resolver is used
				rs.DNS.LocalIP = dns.LocalIp
			}
			rs.DNS.Error = dns.Error
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
	return wt, nil
}

func (s *StatusInfo) WriterTos() []io.WriterTo {
	return []io.WriterTo{s.UserDaemon, s.RootDaemon}
}

func (s *StatusInfoEmbedded) WriterTos() []io.WriterTo {
	return []io.WriterTo{s}
}

func (s *StatusInfoEmbedded) WriteTo(out io.Writer) (int64, error) {
	n := 0
	cs := s.UserDaemon.(*userDaemonStatus)
	if cs.Running {
		n += ioutil.Printf(out, "%s: Running\n", cs.Name)
		kvf := ioutil.DefaultKeyValueFormatter()
		kvf.Prefix = "  "
		kvf.Indent = "  "
		cs.print(kvf)
		if rs, ok := s.RootDaemon.(*rootDaemonStatus); ok && rs.Running {
			rs.printNetwork(kvf)
		}
		n += kvf.Println(out)
	} else {
		n += ioutil.Println(out, "Daemon: Not running")
	}
	return int64(n), nil
}

func (ds *rootDaemonStatus) WriteTo(out io.Writer) (int64, error) {
	n := 0
	if ds.Running {
		n += ioutil.Printf(out, "%s: Running\n", ds.Name)
		kvf := ioutil.DefaultKeyValueFormatter()
		kvf.Prefix = "  "
		kvf.Indent = "  "
		kvf.Add("Version", ds.Version)
		ds.printNetwork(kvf)
		n += kvf.Println(out)
	} else {
		n += ioutil.Println(out, "Root Daemon: Not running")
	}
	return int64(n), nil
}

func (ds *rootDaemonStatus) printNetwork(kvf *ioutil.KeyValueFormatter) {
	kvf.Add("Version", ds.Version)
	if ds.DNS != nil {
		printDNS(kvf, ds.DNS)
	}
	if ds.RoutingSnake != nil {
		printRouting(kvf, ds.RoutingSnake)
	}
}

func printDNS(kvf *ioutil.KeyValueFormatter, d *client.DNSSnake) {
	dnsKvf := ioutil.DefaultKeyValueFormatter()
	kvf.Indent = "  "
	if d.Error != "" {
		dnsKvf.Add("Error", d.Error)
	}
	if len(d.LocalIP) > 0 {
		dnsKvf.Add("Local IP", d.LocalIP.String())
	}
	if len(d.RemoteIP) > 0 {
		dnsKvf.Add("Remote IP", d.RemoteIP.String())
	}
	dnsKvf.Add("Exclude suffixes", fmt.Sprintf("%v", d.ExcludeSuffixes))
	dnsKvf.Add("Include suffixes", fmt.Sprintf("%v", d.IncludeSuffixes))
	dnsKvf.Add("Timeout", fmt.Sprintf("%v", d.LookupTimeout))
	kvf.Add("DNS", "\n"+dnsKvf.String())
}

func printRouting(kvf *ioutil.KeyValueFormatter, r *client.RoutingSnake) {
	printSubnets := func(title string, subnets []*iputil.Subnet) {
		out := &strings.Builder{}
		fmt.Fprintf(out, "(%d subnets)", len(subnets))
		for _, subnet := range subnets {
			ioutil.Printf(out, "\n- %s", subnet)
		}
		kvf.Add(title, out.String())
	}
	printSubnets("Also Proxy", r.AlsoProxy)
	printSubnets("Never Proxy", r.NeverProxy)
}

func (cs *userDaemonStatus) WriteTo(out io.Writer) (int64, error) {
	n := 0
	if cs.Running {
		n += ioutil.Printf(out, "%s: Running\n", cs.Name)
		kvf := ioutil.DefaultKeyValueFormatter()
		kvf.Prefix = "  "
		kvf.Indent = "  "
		cs.print(kvf)
		n += kvf.Println(out)
	} else {
		n += ioutil.Println(out, "User Daemon: Not running")
	}
	return int64(n), nil
}

func (cs *userDaemonStatus) print(kvf *ioutil.KeyValueFormatter) {
	kvf.Add("Version", cs.Version)
	kvf.Add("Executable", cs.Executable)
	kvf.Add("Install ID", cs.InstallID)
	kvf.Add("Status", cs.Status)
	if cs.Error != "" {
		kvf.Add("Error", cs.Error)
	}
	kvf.Add("Kubernetes server", cs.KubernetesServer)
	kvf.Add("Kubernetes context", cs.KubernetesContext)
	kvf.Add("Manager namespace", cs.ManagerNamespace)
	if len(cs.MappedNamespaces) > 0 {
		kvf.Add("Mapped namespaces", fmt.Sprintf("%v", cs.MappedNamespaces))
	}
	out := &strings.Builder{}
	fmt.Fprintf(out, "%d total\n", len(cs.Intercepts))
	if len(cs.Intercepts) > 0 {
		subKvf := ioutil.DefaultKeyValueFormatter()
		subKvf.Indent = "  "
		for _, intercept := range cs.Intercepts {
			subKvf.Add(intercept.Name, intercept.Client)
		}
		subKvf.Println(out)
	}
	kvf.Add("Intercepts", out.String())
}
