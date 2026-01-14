package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strings"

	"github.com/go-json-experiment/json"
	"github.com/spf13/cobra"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	daemonRpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/progress"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

type StatusInfo struct {
	RootDaemon     RootDaemonStatus     `json:"root_daemon"`
	UserDaemon     UserDaemonStatus     `json:"user_daemon"`
	TrafficManager TrafficManagerStatus `json:"traffic_manager"`
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
	Managed      bool             `json:"managed,omitempty"`
	Running      bool             `json:"running,omitempty"`
	Name         string           `json:"name,omitempty"`
	Version      string           `json:"version,omitempty"`
	APIVersion   int32            `json:"api_version,omitempty"`
	PortMappings []string         `json:"port_mappings,omitempty"`
	DNS          *client.DNSSnake `json:"dns,omitempty"`
	*client.RoutingSnake
}

type UserDaemonStatus struct {
	Running           bool                     `json:"running,omitempty"`
	InDocker          bool                     `json:"in_docker,omitempty"`
	Name              string                   `json:"name,omitempty"`
	DaemonPort        int                      `json:"daemon_port,omitempty"`
	ContainerNetwork  string                   `json:"container_network,omitempty"`
	Hostname          string                   `json:"hostname,omitempty"`
	ExposedPorts      []string                 `json:"exposedPorts,omitempty"`
	Version           string                   `json:"version,omitempty"`
	Executable        string                   `json:"executable,omitempty"`
	InstallID         string                   `json:"install_id,omitempty"`
	Status            string                   `json:"status,omitempty"`
	Error             string                   `json:"error,omitempty"`
	KubernetesServer  string                   `json:"kubernetes_server,omitempty"`
	KubernetesContext string                   `json:"kubernetes_context,omitempty"`
	Namespace         string                   `json:"namespace,omitempty"`
	ManagerNamespace  string                   `json:"manager_namespace,omitempty"`
	MappedNamespaces  []string                 `json:"mapped_namespaces,omitempty"`
	Ingests           []ConnectStatusIngest    `json:"ingests,omitempty"`
	Intercepts        []ConnectStatusIntercept `json:"intercepts,omitempty"`
	Replacements      []ConnectStatusIntercept `json:"replacements,omitempty"`
	Wiretaps          []ConnectStatusIntercept `json:"wiretaps,omitempty"`
	versionName       string
}

type ContainerizedDaemonStatus struct {
	*UserDaemonStatus
	PortMappings []string         `json:"port_mappings,omitempty"`
	DNS          *client.DNSSnake `json:"dns,omitempty"`
	*client.RoutingSnake
}

type TrafficManagerStatus struct {
	Name         string `json:"name,omitempty"`
	Version      string `json:"version,omitempty"`
	TrafficAgent string `json:"traffic_agent,omitempty"`
	extendedInfo ioutil.KeyValueProvider
}

type ConnectStatusIngest struct {
	Workload  string `json:"workload,omitempty"`
	Container string `json:"container,omitempty"`
	Mount     string `json:"mount,omitempty"`
}

type ConnectStatusIntercept struct {
	Name   string `json:"name,omitempty"`
	Client string `json:"client,omitempty"`
}

const (
	multiDaemonFlag = "multi-daemon"
	jsonFlag        = "json"
)

func statusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:  "status",
		Args: cobra.NoArgs,

		Short: "Show connectivity status",
		RunE:  run,
		Annotations: map[string]string{
			ann.UserDaemon: ann.Optional,
		},
	}
	flags := cmd.Flags()
	flags.Bool(multiDaemonFlag, false, "always use multi-daemon output format, even if there's only one daemon connected")
	return cmd
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
	defer progress.Stop(ctx)

	var sis []ioutil.WriterTos
	if len(mdErr) > 0 {
		sis = make([]ioutil.WriterTos, len(mdErr))
		for i, info := range mdErr {
			udCtx, err := connect.ExistingDaemon(ctx, info)
			if err != nil {
				return err
			}
			sis[i], err = getStatusInfo(udCtx, info)
			_ = daemon.MustGetUserClient(udCtx).Close()
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
var GetTrafficManagerStatusExtras = func(context.Context, daemon.UserClient) ioutil.KeyValueProvider {
	return nil
}

func (s *StatusInfo) WriterTos() []io.WriterTo {
	if s.UserDaemon.InDocker {
		return []io.WriterTo{
			&ContainerizedDaemonStatus{
				UserDaemonStatus: &s.UserDaemon,
				PortMappings:     s.RootDaemon.PortMappings,
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

func setUserDaemonStatus(ctx context.Context, userD daemon.UserClient, di *daemon.Info, us *UserDaemonStatus) (*connector.ConnectInfo, error) {
	installID, err := client.InstallID(ctx)
	if err != nil {
		return nil, err
	}
	us.InstallID = installID
	us.Running = true
	us.Version = userD.Semver().String()
	us.versionName = userD.Name()
	us.Executable = userD.Executable()
	us.Name = userD.DaemonID().Name

	if userD.Containerized() {
		us.InDocker = true
		us.DaemonPort = userD.DaemonPort()
		if di != nil {
			us.Hostname = di.Hostname
			us.ExposedPorts = di.ExposedPorts
		}
		us.ContainerNetwork = userD.DaemonID().ContainerName()
		if us.versionName == "" {
			us.versionName = "Daemon"
		}
	} else if us.versionName == "" {
		us.versionName = "User daemon"
	}

	status, err := userD.Status(ctx, &empty.Empty{})
	if err != nil {
		err = grpc.FromGRPC(err)
		us.Status = "Not connected"
		us.Error = err.Error()
		return nil, err
	}
	us.Status = "Connected"
	us.KubernetesServer = status.ClusterServer
	us.KubernetesContext = status.ClusterContext
	for _, ig := range status.GetIngests() {
		us.Ingests = append(us.Ingests, ConnectStatusIngest{
			Workload:  ig.Workload,
			Container: ig.Container,
			Mount:     ig.ClientMountPoint,
		})
	}
	for _, icept := range status.GetIntercepts().GetIntercepts() {
		cis := ConnectStatusIntercept{
			Name:   icept.Spec.Name,
			Client: icept.Spec.Client,
		}
		switch {
		case icept.Spec.NoDefaultPort:
			us.Replacements = append(us.Replacements, cis)
		case icept.Spec.Wiretap:
			us.Wiretaps = append(us.Wiretaps, cis)
		default:
			us.Intercepts = append(us.Intercepts, cis)
		}
	}
	us.Namespace = status.Namespace
	us.ManagerNamespace = status.ManagerNamespace
	us.MappedNamespaces = status.MappedNamespaces

	return status, nil
}

func getStatusInfo(ctx context.Context, di *daemon.Info) (*StatusInfo, error) {
	wt := &StatusInfo{}
	var rStatus *daemonRpc.DaemonStatus
	userD := daemon.GetUserClient(ctx)
	if userD != nil {
		status, err := setUserDaemonStatus(ctx, userD, di, &wt.UserDaemon)
		if err != nil {
			return nil, err
		}
		if mv := status.ManagerVersion; mv != nil {
			tm := &wt.TrafficManager
			tm.Name = mv.Name
			tm.Version = mv.Version
			if af, err := userD.AgentImageFQN(ctx, &empty.Empty{}); err == nil {
				tm.TrafficAgent = af.FQN
			}
			tm.extendedInfo = GetTrafficManagerStatusExtras(ctx, userD)
		}
		rStatus = status.DaemonStatus
	} else {
		conn, err := daemon.DialRootDaemon(ctx, false)
		if err != nil {
			return wt, nil
		}
		defer conn.Close()
		if rStatus, err = daemonRpc.NewDaemonClient(conn).Status(ctx, &empty.Empty{}); err != nil {
			return wt, err
		}
	}

	if rStatus == nil {
		return wt, nil
	}

	rs := &wt.RootDaemon
	rs.Running = true
	rs.Managed = rStatus.Managed
	rs.Name = rStatus.Version.Name
	if rs.Name == "" {
		rs.Name = "Root Daemon"
	}
	rs.Version = rStatus.Version.Version
	rs.APIVersion = rStatus.Version.ApiVersion
	if obc := rStatus.OutboundConfig; obc != nil {
		rs.PortMappings = obc.PortMappings
	}
	if rootCfg, err := daemon.GetRootClientConfig(rStatus); err == nil {
		us := &wt.UserDaemon
		rs.DNS = rootCfg.DNS().ToSnake()
		rs.RoutingSnake = rootCfg.Routing().ToSnake()
		if us.InDocker {
			if len(rs.Subnets) == 0 {
				// No teleroute network is started when there are no subnets to route.
				// DNS is exposed on port 53 on the containerized daemon, so the
				// IP that it exposes on the default bridge can be used for DNS.
				rs.DNS.LocalAddresses = []netip.AddrPort{netip.AddrPortFrom(userD.DaemonInfo().ContainerIP, 53)}
				us.ContainerNetwork = "default bridge"
			}
		}
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
	if cs.Running {
		n += ioutil.Printf(out, "%s %s: Running\n", cs.versionName, cs.Name)
		kvf := ioutil.DefaultKeyValueFormatter()
		kvf.Prefix = "  "
		kvf.Indent = "  "
		cs.print(kvf)
		if len(cs.PortMappings) > 0 {
			printPortMappings(kvf, cs.PortMappings)
		}
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
		mgd := ""
		if ds.Managed {
			mgd = " (managed)"
		}
		n += ioutil.Printf(out, "%s%s: Running\n", ds.Name, mgd)
		kvf := ioutil.DefaultKeyValueFormatter()
		kvf.Prefix = "  "
		kvf.Indent = "  "
		kvf.Add("Version", ds.Version)
		if len(ds.PortMappings) > 0 {
			printPortMappings(kvf, ds.PortMappings)
		}
		if ds.DNS != nil {
			printDNS(kvf, ds.DNS)
		}
		if ds.RoutingSnake != nil {
			printRouting(kvf, ds.RoutingSnake)
		}
		n += kvf.Println(out)
	} else {
		n += ioutil.Println(out, "OSS Root Daemon: Not running")
	}
	return int64(n), nil
}

func printPortMappings(kvf *ioutil.KeyValueFormatter, pms []string) {
	pmKvf := ioutil.DefaultKeyValueFormatter()
	for _, pm := range pms {
		ix := strings.LastIndexByte(pm, ':')
		if ix < 0 {
			continue
		}
		pmKvf.Add(pm[:ix], pm[ix+1:])
	}
	kvf.Add("Port Mappings", "\n"+pmKvf.String())
}

func printDNS(kvf *ioutil.KeyValueFormatter, d *client.DNSSnake) {
	dnsKvf := ioutil.DefaultKeyValueFormatter()
	if d.Error != "" {
		dnsKvf.Add("Error", d.Error)
	}
	if len(d.LocalAddresses) > 0 {
		dnsKvf.Add("Local addresses", fmt.Sprintf("%s", d.LocalAddresses))
	}
	if d.VIFAddress.IsValid() {
		dnsKvf.Add("VIF Address", d.VIFAddress.String())
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
	printSubnets := func(title string, subnets []netip.Prefix) {
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
		n += ioutil.Println(out, "OSS User Daemon: Not running")
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
	if il := len(cs.Ingests); il > 0 {
		out := &strings.Builder{}
		ioutil.Printf(out, "%d total\n", il)
		for _, ingest := range cs.Ingests {
			ioutil.Printf(out, "  %s/%s\n", ingest.Workload, ingest.Container)
		}
		kvf.Add("Ingests", out.String())
	}
	addInterceptGroup := func(name string, intercepts []ConnectStatusIntercept) {
		if il := len(intercepts); il > 0 {
			out := &strings.Builder{}
			ioutil.Printf(out, "%d total\n", il)
			subKvf := ioutil.DefaultKeyValueFormatter()
			subKvf.Indent = "  "
			for _, intercept := range intercepts {
				subKvf.Add(intercept.Name, intercept.Client)
			}
			subKvf.Println(out)
			kvf.Add(name, out.String())
		}
	}
	addInterceptGroup("Replacements", cs.Replacements)
	addInterceptGroup("Intercepts", cs.Intercepts)
	addInterceptGroup("Wiretaps", cs.Wiretaps)
}

func (ts *TrafficManagerStatus) MarshalJSON() ([]byte, error) {
	m, err := ts.toMap()
	if err != nil {
		return nil, err
	}
	return json.Marshal(m)
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
