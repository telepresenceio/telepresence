package daemon

import (
	"context"
	"os"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/global"
	"github.com/telepresenceio/telepresence/v2/pkg/slice"
)

type Request struct {
	connector.ConnectRequest
	Docker bool

	// Request is created on-demand, not by InitRequest
	Implicit                bool
	kubeConfig              *genericclioptions.ConfigFlags
	kubeFlagSet             *pflag.FlagSet
	UserDaemonProfilingPort uint16
	RootDaemonProfilingPort uint16
}

// InitRequest adds the networking flags and Kubernetes flags to the given command and
// returns a Request and a FlagSet with the Kubernetes flags. The FlagSet is returned
// here so that a map of flags that gets modified can be extracted using FlagMap once the flag
// parsing has completed.
func InitRequest(cmd *cobra.Command) *Request {
	cr := Request{}
	flags := cmd.Flags()

	nwFlags := pflag.NewFlagSet("Telepresence networking flags", 0)
	nwFlags.StringSliceVar(&cr.MappedNamespaces,
		"mapped-namespaces", nil, ``+
			`Comma separated list of namespaces considered by DNS resolver and NAT for outbound connections. `+
			`Defaults to all namespaces`)
	nwFlags.StringSliceVar(&cr.AlsoProxy,
		"also-proxy", nil, ``+
			`Additional comma separated list of CIDR to proxy`)

	nwFlags.StringSliceVar(&cr.NeverProxy,
		"never-proxy", nil, ``+
			`Comma separated list of CIDR to never proxy`)
	nwFlags.StringVar(&cr.ManagerNamespace, "manager-namespace", "", `The namespace where the traffic manager is to be found. `+
		`Overrides any other manager namespace set in config`)
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
	cr.kubeConfig.Context = nil // --context is global
	cr.KubeFlags = make(map[string]string)
	cr.kubeFlagSet = pflag.NewFlagSet("Kubernetes flags", 0)
	cr.kubeConfig.AddFlags(cr.kubeFlagSet)
	flags.AddFlagSet(cr.kubeFlagSet)
	_ = cmd.RegisterFlagCompletionFunc("namespace", cr.autocompleteNamespace)
	_ = cmd.RegisterFlagCompletionFunc("cluster", cr.autocompleteCluster)
	return &cr
}

type requestKey struct{}

func (cr *Request) CommitFlags(cmd *cobra.Command) {
	cr.kubeFlagSet.VisitAll(func(flag *pflag.Flag) {
		if flag.Changed {
			var v string
			if sv, ok := flag.Value.(pflag.SliceValue); ok {
				v = slice.AsCSV(sv.GetSlice())
			} else {
				v = flag.Value.String()
			}
			cr.KubeFlags[flag.Name] = v
		}
	})
	cr.addKubeconfigEnv()
	cr.setGlobalConnectFlags(cmd)
	cmd.SetContext(context.WithValue(cmd.Context(), requestKey{}, cr))
}

func (cr *Request) addKubeconfigEnv() {
	// Certain options' default are bound to the connector daemon process; this is notably true of the kubeconfig file(s) to use,
	// and since those files can be specified, both as a --kubeconfig flag and in the KUBECONFIG setting, and since the flag won't
	// accept multiple path entries, we need to pass the environment setting to the connector daemon so that it can set it every
	// time it receives a new config.
	addEnv := func(key string) {
		if cfg, ok := os.LookupEnv(key); ok {
			cr.KubeFlags[key] = cfg
		}
	}
	for _, kubeconfigEnv := range client.EnvVarOnlyKubeFlags {
		addEnv(kubeconfigEnv)
	}
}

// setContext deals with the global --context flag and assigns it to KubeFlags because it's
// deliberately excluded from the original flags (to avoid conflict with the global flag).
func (cr *Request) setGlobalConnectFlags(cmd *cobra.Command) {
	if contextFlag := cmd.Flag(global.FlagContext); contextFlag != nil && contextFlag.Changed {
		cn := contextFlag.Value.String()
		cr.KubeFlags[global.FlagContext] = cn
		cr.kubeConfig.Context = &cn
	}
	if dockerFlag := cmd.Flag(global.FlagDocker); dockerFlag != nil && dockerFlag.Changed {
		cr.Docker, _ = strconv.ParseBool(dockerFlag.Value.String())
	}
}

func GetRequest(ctx context.Context) *Request {
	if cr, ok := ctx.Value(requestKey{}).(*Request); ok {
		return cr
	}
	return nil
}

func WithDefaultRequest(ctx context.Context, cmd *cobra.Command) context.Context {
	cr := Request{
		ConnectRequest: connector.ConnectRequest{
			KubeFlags: make(map[string]string),
		},
		Implicit:   true,
		kubeConfig: genericclioptions.NewConfigFlags(false),
	}
	cr.kubeConfig.Context = nil // --context is global

	// Handle deprecated namespace flag, but allow it in the list command.
	if cmd.Use != "list" {
		if nsFlag := cmd.Flag("namespace"); nsFlag != nil && nsFlag.Changed {
			ns := nsFlag.Value.String()
			*cr.kubeConfig.Namespace = ns
			cr.KubeFlags["namespace"] = ns
		}
	}
	cr.setGlobalConnectFlags(cmd)
	cr.addKubeconfigEnv()
	return context.WithValue(ctx, requestKey{}, &cr)
}

func GetKubeStartingConfig(cmd *cobra.Command) (*api.Config, error) {
	pathOpts := clientcmd.NewDefaultPathOptions()
	if kcFlag := cmd.Flag("kubeconfig"); kcFlag != nil && kcFlag.Changed {
		pathOpts.ExplicitFileFlag = kcFlag.Value.String()
	}
	return pathOpts.GetStartingConfig()
}

func (cr *Request) autocompleteNamespace(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	cr.CommitFlags(cmd)
	var ctName string
	if cp := cr.kubeConfig.Context; cp != nil {
		ctName = *cp
	}
	ctx := cmd.Context()
	dlog.Debugf(ctx, "namespace completion: context %q, %q", ctName, toComplete)
	rs, err := cr.kubeConfig.ToRESTConfig()
	if err != nil {
		dlog.Errorf(ctx, "ToRESTConfig: %v", err)
		return nil, cobra.ShellCompDirectiveError
	}
	cs, err := kubernetes.NewForConfig(rs)
	if err != nil {
		dlog.Errorf(ctx, "NewForConfig: %v", err)
		return nil, cobra.ShellCompDirectiveError
	}
	nsl, err := cs.CoreV1().Namespaces().List(ctx, v1.ListOptions{})
	if err != nil {
		dlog.Errorf(ctx, "Namespaces.List: %v", err)
		return nil, cobra.ShellCompDirectiveError
	}
	itms := nsl.Items
	nss := make([]string, len(itms))
	for i, itm := range itms {
		nss[i] = itm.Name
	}
	return nss, cobra.ShellCompDirectiveNoFileComp
}

func (cr *Request) autocompleteCluster(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	cr.CommitFlags(cmd)
	var ctName string
	if cp := cr.kubeConfig.Context; cp != nil {
		ctName = *cp
	}
	ctx := cmd.Context()
	dlog.Debugf(ctx, "namespace completion: context %q, %q", ctName, toComplete)
	cfg, err := GetKubeStartingConfig(cmd)
	if err != nil {
		dlog.Errorf(ctx, "GetKubeStartingConfig: %v", err)
		return nil, cobra.ShellCompDirectiveError
	}
	cxl := cfg.Clusters
	nss := make([]string, len(cxl))
	i := 0
	for n := range cxl {
		nss[i] = n
		i++
	}
	return nss, cobra.ShellCompDirectiveNoFileComp
}
