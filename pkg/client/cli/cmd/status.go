package cmd

import (
	"context"
	"encoding/json"
	"errors"
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
	RootDaemon     RootDaemonStatus     `json:"root_daemon" yaml:"root_daemon"`
	UserDaemon     UserDaemonStatus     `json:"user_daemon" yaml:"user_daemon"`
	TrafficManager TrafficManagerStatus `json:"traffic_manager" yaml:"traffic_manager"`
}

type MultiConnectStatusInfo struct {
	extendedInfo ioutil.WriterTos
	statusInfos  []ioutil.WriterTos
}

type SingleConnectStatusInfo struct {
	extendedInfo ioutil.WriterTos
	statusInfo   ioutil.WriterTos
}

type RootDaemonStatus struct {
	Running              bool             `json:"running,omitempty" yaml:"running,omitempty"`
	Name                 string           `json:"name,omitempty" yaml:"name,omitempty"`
	Version              string           `json:"version,omitempty" yaml:"version,omitempty"`
	APIVersion           int32            `json:"api_version,omitempty" yaml:"api_version,omitempty"`
	DNS                  *client.DNSSnake `json:"dns,omitempty" yaml:"dns,omitempty"`
	*client.RoutingSnake `yaml:",inline"`
}

type UserDaemonStatus struct {
	Running           bool                     `json:"running,omitempty" yaml:"running,omitempty"`
	InDocker          bool                     `json:"in_docker,omitempty" yaml:"in_docker,omitempty"`
	Name              string                   `json:"name,omitempty" yaml:"name,omitempty"`
	DaemonPort        int                      `json:"daemon_port,omitempty" yaml:"daemon_port,omitempty"`
	ContainerNetwork  string                   `json:"container_network,omitempty" yaml:"container_network,omitempty"`
	Hostname          string                   `json:"hostname,omitempty" yaml:"hostname,omitempty"`
	ExposedPorts      []string                 `json:"exposedPorts,omitempty" yaml:"exposedPorts,omitempty"`
	Version           string                   `json:"version,omitempty" yaml:"version,omitempty"`
	Executable        string                   `json:"executable,omitempty" yaml:"executable,omitempty"`
	InstallID         string                   `json:"install_id,omitempty" yaml:"install_id,omitempty"`
	Status            string                   `json:"status,omitempty" yaml:"status,omitempty"`
	Error             string                   `json:"error,omitempty" yaml:"error,omitempty"`
	KubernetesServer  string                   `json:"kubernetes_server,omitempty" yaml:"kubernetes_server,omitempty"`
	KubernetesContext string                   `json:"kubernetes_context,omitempty" yaml:"kubernetes_context,omitempty"`
	Namespace         string                   `json:"namespace,omitempty" yaml:"namespace,omitempty"`
	ManagerNamespace  string                   `json:"manager_namespace,omitempty" yaml:"manager_namespace,omitempty"`
	MappedNamespaces  []string                 `json:"mapped_namespaces,omitempty" yaml:"mapped_namespaces,omitempty"`
	Intercepts        []ConnectStatusIntercept `json:"intercepts,omitempty" yaml:"intercepts,omitempty"`
	versionName       string
}

type ContainerizedDaemonStatus struct {
	*UserDaemonStatus    `yaml:",inline"`
	DNS                  *client.DNSSnake `json:"dns,omitempty" yaml:"dns,omitempty"`
	*client.RoutingSnake `yaml:",inline"`
}

type TrafficManagerStatus struct {
	Name         string `json:"name,omitempty" yaml:"name,omitempty"`
	Version      string `json:"version,omitempty" yaml:"version,omitempty"`
	TrafficAgent string `json:"traffic_agent,omitempty" yaml:"traffic_agent,omitempty"`
	extendedInfo ioutil.KeyValueProvider
}

type ConnectStatusIntercept struct {
	Name   string `json:"name,omitempty" yaml:"name,omitempty"`
	Client string `json:"client,omitempty" yaml:"client,omitempty"`
}

const (
	multiDaemonFlag = "multi-daemon"
	jsonFlag        = "json"
)

func statusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:  "status",
		Args: cobra.NoArgs,

		Short:             "Show connectivity status",
		RunE:              run,
		PersistentPreRunE: fixFlag,
		Annotations: map[string]string{
			ann.UserDaemon: ann.Optional,
		},
	}
	flags := cmd.Flags()
	flags.Bool(multiDaemonFlag, false, "always use multi-daemon output format, even if there's only one daemon connected")
	flags.BoolP(jsonFlag, "j", false, "output as json object")
	flags.Lookup(jsonFlag).Hidden = true
	return cmd
}

func fixFlag(cmd *cobra.Command, _ []string) error {
	flags := cmd.Flags()
	json, err := flags.GetBool(jsonFlag)
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
	var mdErr daemon.MultipleDaemonsError
	err := connect.InitCommand(cmd)
	if err != nil {
		if !errors.As(err, &mdErr) {
			return err
		}
	}
	ctx := cmd.Context()

	var sis []ioutil.WriterTos
	if len(mdErr) > 0 {
		sis = make([]ioutil.WriterTos, len(mdErr))
		for i, info := range mdErr {
			ud, err := connect.ExistingDaemon(ctx, info)
			if err != nil {
				return err
			}
			sis[i], err = getStatusInfo(daemon.WithUserClient(ctx, ud), info)
			ud.Conn.Close()
			if err != nil {
				return err
			}
		}
	} else {
		si, err := getStatusInfo(ctx, nil)
		if err != nil {
			return err
		}
		sis = []ioutil.WriterTos{si}
	}

	sx, err := GetStatusInfo(ctx)
	if err != nil {
		return err
	}

	multiFormat := len(sis) > 1
	if !multiFormat {
		multiFormat, _ = cmd.Flags().GetBool(multiDaemonFlag)
	}
	var as ioutil.WriterTos
	if multiFormat {
		as = &MultiConnectStatusInfo{
			extendedInfo: sx,
			statusInfos:  sis,
		}
	} else {
		as = &SingleConnectStatusInfo{
			extendedInfo: sx,
			statusInfo:   sis[0],
		}
	}

	if output.WantsFormatted(cmd) {
		output.Object(ctx, &as, true)
	} else {
		_, _ = ioutil.WriteAllTo(cmd.OutOrStdout(), as.WriterTos()...)
	}
	return nil
}

// GetStatusInfo may return an extended struct
//
//nolint:gochecknoglobals // extension point
var GetStatusInfo = func(ctx context.Context) (ioutil.WriterTos, error) {
	return nil, nil
}

// GetTrafficManagerStatusExtras may return an extended struct
//
//nolint:gochecknoglobals // extension point
var GetTrafficManagerStatusExtras = func(context.Context, *daemon.UserClient) ioutil.KeyValueProvider {
	return nil
}

func (s *StatusInfo) WriterTos() []io.WriterTo {
	if s.UserDaemon.InDocker {
		return []io.WriterTo{
			&ContainerizedDaemonStatus{
				UserDaemonStatus: &s.UserDaemon,
				DNS:              s.RootDaemon.DNS,
				RoutingSnake:     s.RootDaemon.RoutingSnake,
			},
			&s.TrafficManager,
		}
	}
	return []io.WriterTo{&s.UserDaemon, &s.RootDaemon, &s.TrafficManager}
}

func (s *StatusInfo) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.toMap())
}

func (s *StatusInfo) MarshalYAML() (any, error) {
	return s.toMap(), nil
}

func (s *StatusInfo) toMap() map[string]any {
	if s.UserDaemon.InDocker {
		return map[string]any{
			"daemon": &ContainerizedDaemonStatus{
				UserDaemonStatus: &s.UserDaemon,
				DNS:              s.RootDaemon.DNS,
				RoutingSnake:     s.RootDaemon.RoutingSnake,
			},
			"traffic_manager": &s.TrafficManager,
		}
	}
	return map[string]any{
		"user_daemon":     &s.UserDaemon,
		"root_daemon":     &s.RootDaemon,
		"traffic_manager": &s.TrafficManager,
	}
}

func getStatusInfo(ctx context.Context, di *daemon.Info) (*StatusInfo, error) {
	wt := &StatusInfo{}
	userD := daemon.GetUserClient(ctx)
	if userD == nil {
		return wt, nil
	}
	ctx = scout.NewReporter(ctx, "cli")
	us := &wt.UserDaemon
	us.InstallID = scout.InstallID(ctx)
	us.Running = true
	v, err := userD.Version(ctx, &empty.Empty{})
	if err != nil {
		return nil, err
	}
	us.Version = v.Version
	us.versionName = v.Name
	us.Executable = v.Executable
	us.Name = userD.DaemonID.Name

	if userD.Containerized() {
		us.InDocker = true
		us.DaemonPort = userD.DaemonPort()
		if di != nil {
			us.Hostname = di.Hostname
			us.ExposedPorts = di.ExposedPorts
		}
		us.ContainerNetwork = "container:" + userD.DaemonID.ContainerName()
		if us.versionName == "" {
			us.versionName = "Daemon"
		}
	} else if us.versionName == "" {
		us.versionName = "User daemon"
	}

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
			us.Intercepts = append(us.Intercepts, ConnectStatusIntercept{
				Name:   icept.Spec.Name,
				Client: icept.Spec.Client,
			})
		}
		us.Namespace = status.Namespace
		us.ManagerNamespace = status.ManagerNamespace
		us.MappedNamespaces = status.MappedNamespaces
	case connector.ConnectInfo_UNAUTHORIZED:
		us.Status = "Not authorized to connect"
		us.Error = status.ErrorText
	case connector.ConnectInfo_UNAUTHENTICATED:
		us.Status = "Not logged in"
		us.Error = status.ErrorText
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
		rs := &wt.RootDaemon
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
			rs.DNS.Excludes = dns.Excludes
			rs.DNS.Mappings.FromRPC(dns.Mappings)
			rs.DNS.LookupTimeout = dns.LookupTimeout.AsDuration()
			rs.RoutingSnake = &client.RoutingSnake{}
			for _, subnet := range rStatus.Subnets {
				rs.RoutingSnake.Subnets = append(rs.RoutingSnake.Subnets, (*iputil.Subnet)(iputil.IPNetFromRPC(subnet)))
			}
			for _, subnet := range obc.AlsoProxySubnets {
				rs.RoutingSnake.AlsoProxy = append(rs.RoutingSnake.AlsoProxy, (*iputil.Subnet)(iputil.IPNetFromRPC(subnet)))
			}
			for _, subnet := range obc.NeverProxySubnets {
				rs.RoutingSnake.NeverProxy = append(rs.RoutingSnake.NeverProxy, (*iputil.Subnet)(iputil.IPNetFromRPC(subnet)))
			}
			for _, subnet := range obc.AllowConflictingSubnets {
				rs.RoutingSnake.AllowConflicting = append(rs.RoutingSnake.AllowConflicting, (*iputil.Subnet)(iputil.IPNetFromRPC(subnet)))
			}
		}
	}

	if v, err := userD.TrafficManagerVersion(ctx, &empty.Empty{}); err == nil {
		tm := &wt.TrafficManager
		tm.Name = v.Name
		tm.Version = v.Version
		if af, err := userD.AgentImageFQN(ctx, &empty.Empty{}); err == nil {
			tm.TrafficAgent = af.FQN
		}
		tm.extendedInfo = GetTrafficManagerStatusExtras(ctx, userD)
	}

	return wt, nil
}

func (s *SingleConnectStatusInfo) WriterTos() []io.WriterTo {
	var wts []io.WriterTo
	if s.extendedInfo != nil {
		wts = s.extendedInfo.WriterTos()
	}
	wts = append(wts, s.statusInfo.WriterTos()...)
	return wts
}

func (s *SingleConnectStatusInfo) MarshalJSON() ([]byte, error) {
	m, err := s.toMap()
	if err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

func (s *SingleConnectStatusInfo) MarshalYAML() (any, error) {
	return s.toMap()
}

func (s *SingleConnectStatusInfo) toMap() (map[string]any, error) {
	m := make(map[string]any)
	if s.extendedInfo != nil {
		sx, err := json.Marshal(s.extendedInfo)
		if err != nil {
			return nil, err
		}
		if err = json.Unmarshal(sx, &m); err != nil {
			return nil, err
		}
	}
	sx, err := json.Marshal(s.statusInfo)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(sx, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *MultiConnectStatusInfo) MarshalJSON() ([]byte, error) {
	m, err := s.toMap()
	if err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

func (s *MultiConnectStatusInfo) MarshalYAML() (any, error) {
	return s.toMap()
}

func (s *MultiConnectStatusInfo) toMap() (map[string]any, error) {
	m := make(map[string]any)
	if s.extendedInfo != nil {
		sx, err := json.Marshal(s.extendedInfo)
		if err != nil {
			return nil, err
		}
		if err = json.Unmarshal(sx, &m); err != nil {
			return nil, err
		}
	}
	m["connections"] = s.statusInfos
	return m, nil
}

func (s *MultiConnectStatusInfo) WriterTos() []io.WriterTo {
	var wts []io.WriterTo
	if s.extendedInfo != nil {
		wts = s.extendedInfo.WriterTos()
	}
	for _, v := range s.statusInfos {
		wts = append(wts, v.WriterTos()...)
	}
	return wts
}

func (cs *ContainerizedDaemonStatus) WriteTo(out io.Writer) (int64, error) {
	n := 0
	if cs.UserDaemonStatus.Running {
		n += ioutil.Printf(out, "%s %s: Running\n", cs.UserDaemonStatus.versionName, cs.UserDaemonStatus.Name)
		kvf := ioutil.DefaultKeyValueFormatter()
		kvf.Prefix = "  "
		kvf.Indent = "  "
		cs.print(kvf)
		if cs.DNS != nil {
			printDNS(kvf, cs.DNS)
		}
		if cs.RoutingSnake != nil {
			printRouting(kvf, cs.RoutingSnake)
		}
		n += kvf.Println(out)
	} else {
		n += ioutil.Println(out, "Daemon: Not running")
	}
	return int64(n), nil
}

func (ds *RootDaemonStatus) WriteTo(out io.Writer) (int64, error) {
	n := 0
	if ds.Running {
		n += ioutil.Printf(out, "%s: Running\n", ds.Name)
		kvf := ioutil.DefaultKeyValueFormatter()
		kvf.Prefix = "  "
		kvf.Indent = "  "
		kvf.Add("Version", ds.Version)
		if ds.DNS != nil {
			printDNS(kvf, ds.DNS)
		}
		if ds.RoutingSnake != nil {
			printRouting(kvf, ds.RoutingSnake)
		}
		n += kvf.Println(out)
	} else {
		n += ioutil.Println(out, "Root Daemon: Not running")
	}
	return int64(n), nil
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
	if len(d.Excludes) > 0 {
		dnsKvf.Add("Excludes", fmt.Sprintf("%v", d.Excludes))
	}
	if len(d.Mappings) > 0 {
		mappingsKvf := ioutil.DefaultKeyValueFormatter()
		for i := range d.Mappings {
			mappingsKvf.Add(d.Mappings[i].Name, d.Mappings[i].AliasFor)
		}
		dnsKvf.Add("Mappings", "\n"+mappingsKvf.String())
	}
	dnsKvf.Add("Timeout", fmt.Sprintf("%v", d.LookupTimeout))
	kvf.Add("DNS", "\n"+dnsKvf.String())
}

func printRouting(kvf *ioutil.KeyValueFormatter, r *client.RoutingSnake) {
	printSubnets := func(title string, subnets []*iputil.Subnet) {
		if len(subnets) == 0 {
			return
		}
		out := &strings.Builder{}
		ioutil.Printf(out, "(%d subnets)", len(subnets))
		for _, subnet := range subnets {
			ioutil.Printf(out, "\n- %s", subnet)
		}
		kvf.Add(title, out.String())
	}
	printSubnets("Subnets", r.Subnets)
	printSubnets("Also Proxy", r.AlsoProxy)
	printSubnets("Never Proxy", r.NeverProxy)
	printSubnets("Allow conflicts for", r.AllowConflicting)
}

func (cs *UserDaemonStatus) WriteTo(out io.Writer) (int64, error) {
	n := 0
	if cs.Running {
		n += ioutil.Printf(out, "%s: Running\n", cs.versionName)
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

func (cs *UserDaemonStatus) print(kvf *ioutil.KeyValueFormatter) {
	kvf.Add("Version", cs.Version)
	kvf.Add("Executable", cs.Executable)
	kvf.Add("Install ID", cs.InstallID)
	kvf.Add("Status", cs.Status)
	if cs.Error != "" {
		kvf.Add("Error", cs.Error)
	}
	kvf.Add("Kubernetes server", cs.KubernetesServer)
	kvf.Add("Kubernetes context", cs.KubernetesContext)
	if cs.ContainerNetwork != "" {
		kvf.Add("Container network", cs.ContainerNetwork)
	}
	kvf.Add("Namespace", cs.Namespace)
	kvf.Add("Manager namespace", cs.ManagerNamespace)
	if len(cs.MappedNamespaces) > 0 {
		kvf.Add("Mapped namespaces", fmt.Sprintf("%v", cs.MappedNamespaces))
	}
	if cs.Hostname != "" {
		kvf.Add("Hostname", cs.Hostname)
	}
	if len(cs.ExposedPorts) > 0 {
		kvf.Add("Exposed ports", fmt.Sprintf("%v", cs.ExposedPorts))
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

func (ts *TrafficManagerStatus) MarshalJSON() ([]byte, error) {
	m, err := ts.toMap()
	if err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

func (ts *TrafficManagerStatus) MarshalYAML() (any, error) {
	return ts.toMap()
}

func (ts *TrafficManagerStatus) toMap() (map[string]any, error) {
	m := make(map[string]any)
	if ts.extendedInfo != nil {
		sx, err := json.Marshal(ts.extendedInfo)
		if err != nil {
			return nil, err
		}
		if err = json.Unmarshal(sx, &m); err != nil {
			return nil, err
		}
	}
	m["name"] = ts.Name
	m["traffic_agent"] = ts.TrafficAgent
	m["version"] = ts.Version
	return m, nil
}

func (ts *TrafficManagerStatus) WriteTo(out io.Writer) (int64, error) {
	n := 0
	if ts.Name != "" {
		n += ioutil.Printf(out, "%s: Connected\n", ts.Name)
		kvf := ioutil.DefaultKeyValueFormatter()
		kvf.Prefix = "  "
		kvf.Indent = "  "
		kvf.Add("Version", ts.Version)
		if ts.TrafficAgent != "" {
			kvf.Add("Traffic Agent", ts.TrafficAgent)
		}
		if ts.extendedInfo != nil {
			ts.extendedInfo.AddTo(kvf)
		}
		n += kvf.Println(out)
	} else {
		n += ioutil.Println(out, "Traffic Manager: Not connected")
	}
	return int64(n), nil
}
