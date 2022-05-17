package k8s

import (
	"context"
	"encoding/json"
	"os"

	"github.com/spf13/pflag"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	_ "k8s.io/client-go/plugin/pkg/client/auth" // Important for various cloud provider auth
	"k8s.io/client-go/rest"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

// The dnsConfig is part of the kubeconfigExtension struct
type dnsConfig struct {
	// LocalIP is the address of the local DNS server. This entry is only
	// used on Linux system that are not configured to use systemd-resolved and
	// can be overridden by using the option --dns on the command line and defaults
	// to the first line of /etc/resolv.conf
	LocalIP iputil.IPKey `json:"local-ip,omitempty"`

	// RemoteIP is the address of the cluster's DNS service. It will default
	// to the IP of the kube-dns.kube-system or the dns-default.openshift-dns service.
	RemoteIP iputil.IPKey `json:"remote-ip,omitempty"`

	// ExcludeSuffixes are suffixes for which the DNS resolver will always return
	// NXDOMAIN (or fallback in case of the overriding resolver).
	ExcludeSuffixes []string `json:"exclude-suffixes,omitempty"`

	// IncludeSuffixes are suffixes for which the DNS resolver will always attempt to do
	// a lookup. Includes have higher priority than excludes.
	IncludeSuffixes []string `json:"include-suffixes,omitempty"`

	// The maximum time to wait for a cluster side host lookup.
	LookupTimeout metav1.Duration `json:"lookup-timeout,omitempty"`
}

// The managerConfig is part of the kubeconfigExtension struct. It configures discovery of the traffic manager
type managerConfig struct {
	// Namespace is the name of the namespace where the traffic manager is to be found
	Namespace string `json:"namespace,omitempty"`
}

// kubeconfigExtension is an extension read from the selected kubeconfig Cluster.
type kubeconfigExtension struct {
	DNS        *dnsConfig       `json:"dns,omitempty"`
	AlsoProxy  []*iputil.Subnet `json:"also-proxy,omitempty"`
	NeverProxy []*iputil.Subnet `json:"never-proxy,omitempty"`
	Manager    *managerConfig   `json:"manager,omitempty"`
}

type Config struct {
	kubeconfigExtension
	Namespace   string // default cluster namespace.
	Context     string
	Server      string
	flagMap     map[string]string
	ConfigFlags *genericclioptions.ConfigFlags
	RestConfig  *rest.Config
}

const configExtension = "telepresence.io"

func NewConfig(c context.Context, flagMap map[string]string) (*Config, error) {
	// Namespace option will be passed only when explicitly needed. The k8Cluster is namespace agnostic with
	// respect to this option.
	delete(flagMap, "namespace")

	// The KUBECONFIG entry is a copy of the KUBECONFIG environment variable sent to us from the CLI to give
	// this long-running daemon a chance to update it. Using the --kubeconfig flag to send the info isn't
	// sufficient because that flag doesn't allow for multiple path entries like the KUBECONFIG does.
	if kcEnv, ok := flagMap["KUBECONFIG"]; ok {
		delete(flagMap, "KUBECONFIG")
		if err := os.Setenv("KUBECONFIG", kcEnv); err != nil {
			return nil, err
		}
	} else {
		// If user unsets the KUBECONFIG, we need to do that too
		if err := os.Unsetenv("KUBECONFIG"); err != nil {
			return nil, err
		}
	}

	configFlags := genericclioptions.NewConfigFlags(false)
	flags := pflag.NewFlagSet("", 0)
	configFlags.AddFlags(flags)
	for k, v := range flagMap {
		if err := flags.Set(k, v); err != nil {
			return nil, errcat.User.Newf("error processing kubectl flag --%s=%s: %w", k, v, err)
		}
	}

	configLoader := configFlags.ToRawKubeConfigLoader()
	config, err := configLoader.RawConfig()
	if err != nil {
		return nil, err
	}

	if len(config.Contexts) == 0 {
		return nil, errcat.Config.New("kubeconfig has no context definition")
	}

	ctxName := flagMap["context"]
	if ctxName == "" {
		ctxName = config.CurrentContext
	}

	ctx, ok := config.Contexts[ctxName]
	if !ok {
		return nil, errcat.Config.Newf("context %q does not exist in the kubeconfig", ctxName)
	}

	cluster, ok := config.Clusters[ctx.Cluster]
	if !ok {
		return nil, errcat.Config.Newf("the cluster %q declared in context %q does exists in the kubeconfig", ctx.Cluster, ctxName)
	}

	restConfig, err := configLoader.ClientConfig()
	if err != nil {
		return nil, err
	}

	namespace := ctx.Namespace
	if namespace == "" {
		namespace = "default"
	}

	k := &Config{
		Context:     ctxName,
		Server:      cluster.Server,
		Namespace:   namespace,
		flagMap:     flagMap,
		ConfigFlags: configFlags,
		RestConfig:  restConfig,
	}

	if ext, ok := cluster.Extensions[configExtension].(*runtime.Unknown); ok {
		if err = json.Unmarshal(ext.Raw, &k.kubeconfigExtension); err != nil {
			return nil, errcat.Config.Newf("unable to parse extension %s in kubeconfig: %w", configExtension, err)
		}
	}

	if k.kubeconfigExtension.Manager == nil {
		k.kubeconfigExtension.Manager = &managerConfig{}
	}

	if k.kubeconfigExtension.Manager.Namespace == "" {
		k.kubeconfigExtension.Manager.Namespace = client.GetEnv(c).ManagerNamespace
	}

	return k, nil
}

// ContextServiceAndFlagsEqual determines if this instance is equal to the given instance with respect to context,
// server, and flag arguments.
func (kf *Config) ContextServiceAndFlagsEqual(okf *Config) bool {
	return kf != nil && okf != nil &&
		kf.Context == okf.Context &&
		kf.Server == okf.Server &&
		mapEqual(kf.flagMap, okf.flagMap)
}

func (kf *Config) GetManagerNamespace() string {
	return kf.kubeconfigExtension.Manager.Namespace
}

func mapEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if v != b[k] {
			return false
		}
	}
	return true
}
