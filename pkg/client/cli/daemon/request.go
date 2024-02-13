package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/global"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/slice"
)

type Request struct {
	connector.ConnectRequest

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
	kubeFlagSet             *pflag.FlagSet
	UserDaemonProfilingPort uint16
	RootDaemonProfilingPort uint16

	// proxyVia holds the string version for the --proxy-via flag values.
	proxyVia []string
}

// InitRequest adds the networking flags and Kubernetes flags to the given command and
// returns a Request and a FlagSet with the Kubernetes flags. The FlagSet is returned
// here so that a map of flags that gets modified can be extracted using FlagMap once the flag
// parsing has completed.
func InitRequest(cmd *cobra.Command) *Request {
	cr := Request{}
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
	nwFlags.StringSliceVar(&cr.proxyVia,
		"proxy-via", nil, ``+
			`Locally translate cluster DNS responses matching CIDR to virtual IPs that are routed (with reverse `+
			`translation) via WORKLOAD. Must be in the form CIDR=WORKLOAD. CIDR can be substituted for the symblic name "service", "pods", "also", or "all".`)
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
	_ = cmd.RegisterFlagCompletionFunc("namespace", cr.autocompleteNamespace)
	_ = cmd.RegisterFlagCompletionFunc("cluster", cr.autocompleteCluster)
	return &cr
}

type requestKey struct{}

func (cr *Request) CommitFlags(cmd *cobra.Command) error {
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
	cr.addKubeconfigEnv()
	err = cr.setGlobalConnectFlags(cmd)
	if err != nil {
		return errcat.User.New(err)
	}
	cr.SubnetViaWorkloads, err = parseProxyVias(cr.proxyVia)
	if err != nil {
		return errcat.User.New(err)
	}
	cmd.SetContext(context.WithValue(cmd.Context(), requestKey{}, cr))
	return nil
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
			return err
		}
	}
	return nil
}

func GetRequest(ctx context.Context) *Request {
	if cr, ok := ctx.Value(requestKey{}).(*Request); ok {
		return cr
	}
	return nil
}

func WithDefaultRequest(ctx context.Context, cmd *cobra.Command) (context.Context, error) {
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
		ConnectRequest: connector.ConnectRequest{
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

func (cr *Request) GetAllNamespaces(cmd *cobra.Command) ([]string, error) {
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

func (cr *Request) autocompleteNamespace(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	ctx := cmd.Context()
	nss, err := cr.GetAllNamespaces(cmd)
	if err != nil {
		dlog.Error(ctx, err)
		return nil, cobra.ShellCompDirectiveError
	}

	var ctName string
	if cp := cr.kubeConfig.Context; cp != nil {
		ctName = *cp
	}
	dlog.Debugf(ctx, "namespace completion: context %q, %q", ctName, toComplete)

	return nss, cobra.ShellCompDirectiveNoFileComp
}

func (cr *Request) autocompleteCluster(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	ctx := cmd.Context()
	config, err := cr.GetConfig(cmd)
	if err != nil {
		dlog.Error(ctx, err)
		return nil, cobra.ShellCompDirectiveError
	}

	var ctName string
	if cp := cr.kubeConfig.Context; cp != nil {
		ctName = *cp
	}
	dlog.Debugf(ctx, "namespace completion: context %q, %q", ctName, toComplete)

	cxl := config.Clusters
	cs := make([]string, len(cxl))
	i := 0
	for n := range cxl {
		cs[i] = n
		i++
	}
	return cs, cobra.ShellCompDirectiveNoFileComp
}

func (cr *Request) GetConfig(cmd *cobra.Command) (*api.Config, error) {
	if err := cr.CommitFlags(cmd); err != nil {
		return nil, err
	}
	cfg, err := GetKubeStartingConfig(cmd)
	if err != nil {
		return nil, errcat.NoDaemonLogs.Newf("GetKubeStartingConfig: %v", err)
	}
	return cfg, nil
}
