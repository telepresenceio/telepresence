package connect

import (
	"context"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
)

type Request struct {
	connector.ConnectRequest
	Docker bool
}

// InitRequest adds the networking flags and Kubernetes flags to the given command and
// returns a Request and a FlagSet with the Kubernetes flags. The FlagSet is returned
// here so that a map of flags that gets modified can be extracted using FlagMap once the flag
// parsing has completed.
func InitRequest(ctx context.Context, cmd *cobra.Command) (*Request, *pflag.FlagSet) {
	cr := Request{}
	flags := cmd.Flags()

	nwFlags := pflag.NewFlagSet("Telepresence networking flags", 0)
	nwFlags.BoolVar(&cr.Docker, "docker", false, "Start daemon in a docker container")
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

	kubeConfig := genericclioptions.NewConfigFlags(false)
	kubeFlags := pflag.NewFlagSet("Kubernetes flags", 0)
	kubeConfig.AddFlags(kubeFlags)
	flags.AddFlagSet(kubeFlags)
	return &cr, kubeFlags
}

func (cr *Request) AddKubeconfigEnv() {
	// Certain options' default are bound to the connector daemon process; this is notably true of the kubeconfig file(s) to use,
	// and since those files can be specified, both as a --kubeconfig flag and in the KUBECONFIG setting, and since the flag won't
	// accept multiple path entries, we need to pass the environment setting to the connector daemon so that it can set it every
	// time it receives a new config.
	addEnv := func(key string) {
		if cfg, ok := os.LookupEnv(key); ok {
			if cr.KubeFlags == nil {
				cr.KubeFlags = make(map[string]string)
			}
			cr.KubeFlags[key] = cfg
		}
	}
	addEnv("KUBECONFIG")
	addEnv("GOOGLE_APPLICATION_CREDENTIALS")
}

type requestKey struct{}

func WithRequest(ctx context.Context, rq *Request) context.Context {
	return context.WithValue(ctx, requestKey{}, rq)
}

func GetRequest(ctx context.Context) *Request {
	if cr, ok := ctx.Value(requestKey{}).(*Request); ok {
		return cr
	}
	return nil
}
