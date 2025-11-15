package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"google.golang.org/protobuf/proto"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/telepresenceio/dlib/v2/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/global"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
	"github.com/telepresenceio/telepresence/v2/pkg/slice"
)

type Request struct {
	*connector.ConnectRequest

	// If set, then use a containerized daemon for the connection.
	Docker bool

	// Ports exposed by a containerized daemon. Only valid when Docker == true
	ExposedPorts []string

	// Hostname used by a containerized daemon. Only valid when Docker == true
	Hostname string

	// Match expression to use when finding an existing connection by name
	Use *regexp.Regexp

	// Request is created on-demand, not by InitRequest
	Implicit bool

	kubeConfig              *genericclioptions.ConfigFlags
	UserDaemonProfilingPort uint16
	RootDaemonProfilingPort uint16

	// proxyVia holds the string version for the --proxy-via flag values.
	proxyVia []string

	// vnats holds the string version for the --vnat flag values.
	vnats []string

	// LocalReroutes maps ports on localhost to remote ports.
	LocalReroutes []string

	// RemoteReroutes uses the VIF to reroute remote host ports.
	RemoteReroutes []string

	// Aliases by which this daemon can be referenced (added to the telepresence network).
	NetworkAliases []string
}

type CobraRequest struct {
	Request
	kubeFlagSet *pflag.FlagSet
}

// InitRequest adds the networking flags and Kubernetes flags to the given command and
// returns a Request and a FlagSet with the Kubernetes flags. The FlagSet is returned
// here so that a map of flags that gets modified can be extracted using FlagMap once the flag
// parsing has completed.
func InitRequest(cmd *cobra.Command) *CobraRequest {
	cr := CobraRequest{
		Request: Request{
			ConnectRequest: &connector.ConnectRequest{},
		},
	}
	flags := cmd.Flags()

	nwFlags := pflag.NewFlagSet("Telepresence networking flags", 0)
	nwFlags.StringVar(&cr.Name, "name", "", "Optional name to use for the connection")
	nwFlags.StringSliceVar(&cr.MappedNamespaces,
		"mapped-namespaces", nil, ``+
			`Comma separated list of namespaces considered by DNS resolver and NAT for outbound connections. `+
			`Defaults to all namespaces`)
	nwFlags.StringVar(&cr.ManagerNamespace, "manager-namespace", "", `The namespace where the traffic manager is to be found. `+
		`Overrides any other manager namespace set in config`)
	nwFlags.StringSliceVar(&cr.AlsoProxy,
		"also-proxy", nil, ``+
			`Additional comma separated list of CIDR to proxy`)
	nwFlags.StringSliceVar(&cr.NeverProxy,
		"never-proxy", nil, ``+
			`Comma separated list of CIDR to never proxy`)
	nwFlags.StringSliceVar(&cr.vnats,
		"vnat", nil, ``+
			`Use Network Address Translation to create virtual IPs for the given CIDR. CIDR can be substituted for the `+
			`symblic name "service", "pods", "also", or "all".`)
	nwFlags.StringSliceVar(&cr.LocalReroutes,
		"reroute-local", nil, ``+
			`Reroute port on local host to remote host. Format is <local port>:<host>:<port>[/{tcp,udp}]. `+
			`<port> can be symbolic when <host> is a service name.`)
	nwFlags.StringSliceVar(&cr.RemoteReroutes,
		"reroute-remote", nil, ``+
			`Reroute port on remote host. Format is <host>:<port>:<new port>[/{tcp,udp}]. `+
			`<port> can be symbolic when <host> is a service name.`)
	nwFlags.StringSliceVar(&cr.proxyVia,
		"proxy-via", nil, ``+
			`Use Network Address Translation to create virtual IPs for the given CIDR, and route via WORKLOAD. Must be in the `+
			`form CIDR=WORKLOAD. CIDR can be substituted for the symblic name "service", "pods", "also", or "all".`)
	nwFlags.StringSliceVar(&cr.AllowConflictingSubnets,
		"allow-conflicting-subnets", nil, ``+
			`Comma separated list of CIDR that will be allowed to conflict with local subnets`)

	// Docker flags
	nwFlags.Bool(global.FlagDocker, false, "Start, or connect to, daemon in a docker container")
	nwFlags.StringArrayVar(&cr.ExposedPorts,
		"expose", nil, ``+
			`Port that a containerized daemon will expose. See docker run -p for more info. Can be repeated`)
	nwFlags.StringVar(&cr.Hostname,
		"hostname", "", ``+
			`Hostname used by a containerized daemon`)

	flags.AddFlagSet(nwFlags)

	dbgFlags := pflag.NewFlagSet("Debug and Profiling flags", 0)
	dbgFlags.Uint16Var(&cr.UserDaemonProfilingPort,
		"userd-profiling-port", 0, "Start a pprof server in the user daemon on this port")
	_ = dbgFlags.MarkHidden("userd-profiling-port")
	dbgFlags.Uint16Var(&cr.RootDaemonProfilingPort,
		"rootd-profiling-port", 0, "Start a pprof server in the root daemon on this port")
	_ = dbgFlags.MarkHidden("rootd-profiling-port")
	flags.AddFlagSet(dbgFlags)

	cr.kubeConfig = genericclioptions.NewConfigFlags(false)
	cr.KubeFlags = make(map[string]string)
	cr.kubeFlagSet = pflag.NewFlagSet("Kubernetes flags", 0)
	cr.kubeConfig.AddFlags(cr.kubeFlagSet)
	flags.AddFlagSet(cr.kubeFlagSet)
	_ = cmd.RegisterFlagCompletionFunc("mapped-namespaces", cr.autocompleteNamespaces)
	_ = cmd.RegisterFlagCompletionFunc("manager-namespace", cr.autocompleteNamespace)
	_ = cmd.RegisterFlagCompletionFunc("namespace", cr.autocompleteNamespace)
	_ = cmd.RegisterFlagCompletionFunc("cluster", cr.autocompleteCluster)
	return &cr
}

type requestKey struct{}

func (cr *CobraRequest) CommitFlags(cmd *cobra.Command) error {
	var err error
	cr.kubeFlagSet.VisitAll(func(flag *pflag.Flag) {
		if flag.Changed {
			var v string
			if sv, ok := flag.Value.(pflag.SliceValue); ok {
				v = slice.AsCSV(sv.GetSlice())
			} else {
				v = flag.Value.String()
				if flag.Name == "kubeconfig" && v == "-" {
					// Read kubeconfig from stdin
					cr.KubeconfigData, err = io.ReadAll(cmd.InOrStdin())
					return // kubernetes will not understand "-"
				}
			}
			cr.KubeFlags[flag.Name] = v
		}
	})
	if err != nil {
		return err
	}

	// A --vnat CIDR is the same as --proxy-via CIDR=local
	for _, vnat := range cr.vnats {
		cr.proxyVia = append(cr.proxyVia, vnat+"=local")
	}

	err = cr.setGlobalConnectFlags(cmd)
	if err != nil {
		return errcat.User.New(err)
	}
	ctx, err := cr.Commit(cmd.Context())
	if err != nil {
		return err
	}
	cmd.SetContext(ctx)
	return nil
}

func (cr *Request) Commit(ctx context.Context) (context.Context, error) {
	cr.addKubeconfigEnv()
	var err error
	cr.SubnetViaWorkloads, err = parseProxyVias(cr.proxyVia)
	if err != nil {
		return ctx, errcat.User.New(err)
	}
	if len(cr.KubeconfigData) > 0 {
		kc, err := clientcmd.Load(cr.KubeconfigData)
		if err != nil {
			return ctx, fmt.Errorf("unable to parse kubeconfig: %w", err)
		}
		if cr.KubeFlags == nil {
			cr.KubeFlags = make(map[string]string)
		}
		if _, ok := cr.KubeFlags["context"]; !ok {
			cr.KubeFlags["context"] = kc.CurrentContext
		}
		if _, ok := cr.KubeFlags["namespace"]; !ok {
			if currCtx, ok := kc.Contexts[kc.CurrentContext]; ok {
				cr.KubeFlags["namespace"] = currCtx.Namespace
			}
		}
		// kubernetes will not understand "-"
		delete(cr.KubeFlags, "kubeconfig")
	}
	return context.WithValue(ctx, requestKey{}, cr), nil
}

type prefixViaWL struct {
	subnet   netip.Prefix
	symbolic string
	workload string
}

func parseProxyVias(proxyVia []string) ([]*daemon.SubnetViaWorkload, error) {
	l := len(proxyVia)
	if l == 0 {
		return nil, nil
	}
	pvs := make([]prefixViaWL, 0, l)
	for _, dps := range proxyVia {
		dp, err := parseSubnetViaWorkload(dps)
		if err != nil {
			return nil, err
		}
		lastPvs := len(pvs) - 1
		switch dp.symbolic {
		case "":
			for pi := lastPvs; pi >= 0; pi-- {
				pv := pvs[pi]
				if pv.symbolic == "" && pv.subnet.Overlaps(dp.subnet) {
					return nil, fmt.Errorf("CIDRs %s and %s are overlapping", pv.subnet, dp.subnet)
				}
			}
			pvs = append(pvs, dp)
		case "all":
			for pi := lastPvs; pi >= 0; pi-- {
				pv := pvs[pi]
				if pv.symbolic != "" {
					return nil, fmt.Errorf("CIDRs %s and %s are overlapping", pv.symbolic, dp.symbolic)
				}
			}
			// Normalize by replacing "all" with "also", "pods", and "service"
			for _, sym := range []string{"also", "pods", "service"} {
				pvs = append(pvs,
					prefixViaWL{
						symbolic: sym,
						workload: dp.workload,
					})
			}
		default:
			for pi := lastPvs; pi >= 0; pi-- {
				pv := pvs[pi]
				if pv.symbolic == dp.symbolic {
					return nil, fmt.Errorf("CIDRs %s and %s are overlapping", pv.symbolic, dp.symbolic)
				}
			}
			pvs = append(pvs, dp)
		}
	}
	svs := make([]*daemon.SubnetViaWorkload, len(pvs))
	for i, pv := range pvs {
		n := pv.symbolic
		if n == "" {
			n = pv.subnet.String()
		}
		svs[i] = &daemon.SubnetViaWorkload{
			Subnet:   n,
			Workload: pv.workload,
		}
	}
	return svs, nil
}

func parseSubnetViaWorkload(dps string) (prefixViaWL, error) {
	var pv prefixViaWL
	eqIdx := strings.IndexByte(dps, '=')
	if eqIdx <= 0 {
		return pv, fmt.Errorf("--proxy-via %q is not in the format CIDR=WORKLOAD", dps)
	}
	lhs := dps[:eqIdx]
	rhs := dps[eqIdx+1:]
	if errs := validation.IsDNS1123Label(rhs); len(errs) > 0 {
		return pv, errors.New(errs[0])
	}
	if sn, err := netip.ParsePrefix(lhs); err != nil {
		if !(lhs == "all" || lhs == "also" || lhs == "pods" || lhs == "service") {
			return pv, err
		}
		pv.symbolic = lhs
	} else {
		pv.subnet = sn
	}
	pv.workload = rhs
	return pv, nil
}

func (cr *Request) addKubeconfigEnv() {
	// Certain options' default are bound to the connector daemon process; this is notably true of the kubeconfig file(s) to use,
	// and since those files can be specified, both as a --kubeconfig flag and in the KUBECONFIG setting, and since the flag won't
	// accept multiple path entries, we need to pass the environment setting to the connector daemon so that it can set it every
	// time it receives a new config.
	cr.Environment = make(map[string]string, 2)
	addEnv := func(key string) {
		if v, ok := os.LookupEnv(key); ok {
			cr.Environment[key] = v
		} else {
			// A dash prefix in the key means "unset".
			cr.Environment["-"+key] = ""
		}
	}
	addEnv("KUBECONFIG")
	addEnv("GOOGLE_APPLICATION_CREDENTIALS")
}

// setContext deals with the global --context flag and assigns it to KubeFlags because it's
// deliberately excluded from the original flags (to avoid conflict with the global flag).
func (cr *Request) setGlobalConnectFlags(cmd *cobra.Command) error {
	if contextFlag := cmd.Flag(global.FlagContext); contextFlag != nil && contextFlag.Changed {
		cn := contextFlag.Value.String()
		cr.KubeFlags[global.FlagContext] = cn
		cr.kubeConfig.Context = &cn
	}
	if dockerFlag := cmd.Flag(global.FlagDocker); dockerFlag != nil && dockerFlag.Changed {
		cr.Docker, _ = strconv.ParseBool(dockerFlag.Value.String())
	}
	if useFlag := cmd.Flag(global.FlagUse); useFlag != nil && useFlag.Changed {
		var err error
		if cr.Use, err = regexp.Compile(useFlag.Value.String()); err != nil {
			return errcat.User.Newf("argument to --use must be a valid regexp: %v", err)
		}
	}
	return nil
}

func (cr *Request) Clone() *Request {
	cl := *cr
	cl.ConnectRequest = proto.Clone(cr.ConnectRequest).(*connector.ConnectRequest)
	cl.AllowConflictingSubnets = slices.Clone(cr.AllowConflictingSubnets)
	cl.AlsoProxy = slices.Clone(cr.AlsoProxy)
	cl.ContainerKubeFlagOverrides = maps.Copy(cr.ContainerKubeFlagOverrides)
	cl.Environment = maps.Copy(cl.Environment)
	cl.ExposedPorts = slices.Clone(cr.ExposedPorts)
	cl.KubeFlags = maps.Copy(cl.KubeFlags)
	cl.MappedNamespaces = slices.Clone(cr.MappedNamespaces)
	cl.NeverProxy = slices.Clone(cr.NeverProxy)
	cl.SubnetViaWorkloads = slices.Clone(cr.SubnetViaWorkloads)
	cl.proxyVia = slices.Clone(cr.proxyVia)
	return &cl
}

func GetRequest(ctx context.Context) *Request {
	if cr, ok := ctx.Value(requestKey{}).(*Request); ok {
		return cr
	}
	return nil
}

func MustGetRequest(ctx context.Context) *Request {
	rq := GetRequest(ctx)
	if rq != nil {
		return rq
	}
	panic("no request in context")
}

func WithDefaultRequest(cmd *cobra.Command) (context.Context, error) {
	cr := NewDefaultRequest()
	cr.Implicit = true
	cr.kubeConfig.Context = nil // --context is global

	// Handle deprecated namespace flag, but allow it in the list command.
	if cmd.Name() != "list" {
		if nsFlag := cmd.Flag("namespace"); nsFlag != nil && nsFlag.Changed {
			ns := nsFlag.Value.String()
			*cr.kubeConfig.Namespace = ns
			cr.KubeFlags["namespace"] = ns
		}
	}
	ctx := cmd.Context()
	if err := cr.setGlobalConnectFlags(cmd); err != nil {
		return ctx, err
	}
	return WithRequest(ctx, cr), nil
}

func WithRequest(ctx context.Context, cr *Request) context.Context {
	return context.WithValue(ctx, requestKey{}, cr)
}

func NewDefaultRequest() *Request {
	cr := Request{
		ConnectRequest: &connector.ConnectRequest{
			KubeFlags: make(map[string]string),
		},
		kubeConfig: genericclioptions.NewConfigFlags(false),
	}
	cr.addKubeconfigEnv()
	return &cr
}

func GetKubeStartingConfig(cmd *cobra.Command) (*api.Config, error) {
	pathOpts := clientcmd.NewDefaultPathOptions()
	if kcFlag := cmd.Flag("kubeconfig"); kcFlag != nil && kcFlag.Changed {
		pathOpts.ExplicitFileFlag = kcFlag.Value.String()
	}
	return pathOpts.GetStartingConfig()
}

func (cr *CobraRequest) GetAllNamespaces(cmd *cobra.Command) ([]string, error) {
	if err := cr.CommitFlags(cmd); err != nil {
		return nil, err
	}
	rs, err := cr.kubeConfig.ToRESTConfig()
	if err != nil {
		return nil, errcat.NoDaemonLogs.Newf("ToRESTConfig: %v", err)
	}
	cs, err := kubernetes.NewForConfig(rs)
	if err != nil {
		return nil, errcat.NoDaemonLogs.Newf("NewForConfig: %v", err)
	}
	nsl, err := cs.CoreV1().Namespaces().List(cmd.Context(), v1.ListOptions{})
	if err != nil {
		return nil, errcat.NoDaemonLogs.Newf("Namespaces.List: %v", err)
	}
	itms := nsl.Items
	nss := make([]string, len(itms))
	for i, itm := range itms {
		nss[i] = itm.Name
	}
	return nss, nil
}

func (cr *CobraRequest) autocompleteNamespace(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	dlog.Debugf(cmd.Context(), "autocompleteNamespace %q", toComplete)
	var stripFunc func(s string) bool
	if toComplete != "" {
		stripFunc = func(s string) bool { return !strings.HasPrefix(s, toComplete) }
	}
	return cr.autocompleteNamespaceFunc(cmd, stripFunc)
}

func (cr *CobraRequest) autocompleteNamespaces(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	var stripFunc func(s string) bool
	var pfx string
	if toComplete != "" {
		found := strings.Split(toComplete, ",")
		ll := len(found) - 1
		last := found[ll]
		if ll > 0 {
			pfx = strings.Join(found[:ll], ",") + ","
			if last != "" {
				stripFunc = func(s string) bool { return slices.Contains(found, s) || !strings.HasPrefix(s, last) }
			} else {
				stripFunc = func(s string) bool { return slices.Contains(found, s) }
			}
		} else {
			stripFunc = func(s string) bool { return !strings.HasPrefix(s, last) }
		}
	}
	nss, d := cr.autocompleteNamespaceFunc(cmd, stripFunc)
	if pfx != "" {
		for i, ns := range nss {
			nss[i] = pfx + ns
		}
	}
	return nss, d
}

func (cr *CobraRequest) autocompleteNamespaceFunc(cmd *cobra.Command, stripFunc func(s string) bool) ([]string, cobra.ShellCompDirective) {
	ctx := cmd.Context()
	nss, err := cr.GetAllNamespaces(cmd)
	if err != nil {
		dlog.Error(ctx, err)
		return nil, cobra.ShellCompDirectiveError
	}
	if stripFunc != nil {
		nss = slices.DeleteFunc(nss, stripFunc)
	}
	return nss, cobra.ShellCompDirectiveNoFileComp
}

func (cr *CobraRequest) autocompleteCluster(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	ctx := cmd.Context()
	config, err := cr.GetConfig(cmd)
	if err != nil {
		dlog.Error(ctx, err)
		return nil, cobra.ShellCompDirectiveError
	}

	cxl := config.Clusters
	cs := make([]string, len(cxl))
	i := 0
	for n := range cxl {
		cs[i] = n
		i++
	}
	return cs, cobra.ShellCompDirectiveNoFileComp
}

func (cr *CobraRequest) GetConfig(cmd *cobra.Command) (*api.Config, error) {
	if err := cr.CommitFlags(cmd); err != nil {
		return nil, err
	}
	cfg, err := GetKubeStartingConfig(cmd)
	if err != nil {
		return nil, errcat.NoDaemonLogs.Newf("GetKubeStartingConfig: %v", err)
	}
	return cfg, nil
}
