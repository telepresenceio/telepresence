package client

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/pflag"
	"gopkg.in/yaml.v3"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	_ "k8s.io/client-go/plugin/pkg/client/auth" // Important for various cloud provider auth
	"k8s.io/client-go/rest"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
)

// The DnsConfig is part of the KubeconfigExtension struct.
type DnsConfig struct {
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
	LookupTimeout v1.Duration `json:"lookup-timeout,omitempty"`
}

// The ManagerConfig is part of the KubeconfigExtension struct. It configures discovery of the traffic manager.
type ManagerConfig struct {
	// Namespace is the name of the namespace where the traffic manager is to be found
	Namespace string `json:"namespace,omitempty"`
}

// KubeconfigExtension is an extension read from the selected kubeconfig Cluster.
type KubeconfigExtension struct {
	DNS        *DnsConfig       `json:"dns,omitempty"`
	AlsoProxy  []*iputil.Subnet `json:"also-proxy,omitempty"`
	NeverProxy []*iputil.Subnet `json:"never-proxy,omitempty"`
	Manager    *ManagerConfig   `json:"manager,omitempty"`
}

type Kubeconfig struct {
	KubeconfigExtension
	Namespace   string // default cluster namespace.
	Context     string
	Server      string
	FlagMap     map[string]string
	ConfigFlags *genericclioptions.ConfigFlags
	RestConfig  *rest.Config
}

const configExtension = "telepresence.io"

func NewKubeconfig(c context.Context, flagMap map[string]string) (*Kubeconfig, error) {
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
		dlog.Debugf(c, "Using KUBECONFIG %s", kcEnv)
	} else {
		// If user unsets the KUBECONFIG, we need to do that too
		if err := os.Unsetenv("KUBECONFIG"); err != nil {
			return nil, err
		}
	}
	dlog.Debugf(c, "Using kubernetes flags %v", flagMap)

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

	k := &Kubeconfig{
		Context:     ctxName,
		Server:      cluster.Server,
		Namespace:   namespace,
		FlagMap:     flagMap,
		ConfigFlags: configFlags,
		RestConfig:  restConfig,
	}

	if ext, ok := cluster.Extensions[configExtension].(*runtime.Unknown); ok {
		if err = json.Unmarshal(ext.Raw, &k.KubeconfigExtension); err != nil {
			return nil, errcat.Config.Newf("unable to parse extension %s in kubeconfig: %w", configExtension, err)
		}
	}

	if k.KubeconfigExtension.Manager == nil {
		k.KubeconfigExtension.Manager = &ManagerConfig{}
	}

	if k.KubeconfigExtension.Manager.Namespace == "" {
		k.KubeconfigExtension.Manager.Namespace = GetEnv(c).ManagerNamespace
	}

	return k, nil
}

// This represents an inClusterConfig.
func NewInClusterConfig(c context.Context, flagMap map[string]string) (*Kubeconfig, error) {
	// Namespace option will be passed only when explicitly needed. The k8Cluster is namespace agnostic with
	// respect to this option.
	delete(flagMap, "namespace")

	configFlags := genericclioptions.NewConfigFlags(false)
	flags := pflag.NewFlagSet("", 0)
	configFlags.AddFlags(flags)
	for k, v := range flagMap {
		if err := flags.Set(k, v); err != nil {
			return nil, errcat.User.Newf("error processing kubectl flag --%s=%s: %w", k, v, err)
		}
	}

	configLoader := configFlags.ToRawKubeConfigLoader()
	restConfig, err := configLoader.ClientConfig()
	if err != nil {
		return nil, err
	}

	namespace, ok, err := configLoader.Namespace()
	if err != nil || !ok {
		namespace = "default"
	}

	return &Kubeconfig{
		Namespace:   namespace,
		Server:      restConfig.Host,
		FlagMap:     flagMap,
		ConfigFlags: configFlags,
		RestConfig:  restConfig,
		// it may be empty, but we should avoid nil deref
		KubeconfigExtension: KubeconfigExtension{
			Manager: &ManagerConfig{
				Namespace: GetEnv(c).ManagerNamespace,
			},
		},
	}, nil
}

// ContextServiceAndFlagsEqual determines if this instance is equal to the given instance with respect to context,
// server, and flag arguments.
func (kf *Kubeconfig) ContextServiceAndFlagsEqual(okf *Kubeconfig) bool {
	return kf != nil && okf != nil &&
		kf.Context == okf.Context &&
		kf.Server == okf.Server &&
		maps.Equal(kf.FlagMap, okf.FlagMap)
}

func (kf *Kubeconfig) GetManagerNamespace() string {
	return kf.KubeconfigExtension.Manager.Namespace
}

func (kf *Kubeconfig) GetRestConfig() *rest.Config {
	return kf.RestConfig
}

func (kf *Kubeconfig) AddRemoteKubeConfigExtension(ctx context.Context, cfgYaml []byte) error {
	dlog.Debugf(ctx, "Applying remote dns and routing: %s", cfgYaml)
	remote := struct {
		DNS     *DNS     `yaml:"dns,omitempty"`
		Routing *Routing `yaml:"routing,omitempty"`
	}{}
	if err := yaml.Unmarshal(cfgYaml, &remote); err != nil {
		return fmt.Errorf("unable to parse remote kubeconfig: %w", err)
	}
	if kf.DNS == nil {
		kf.DNS = &DnsConfig{}
	}
	if dns := remote.DNS; dns != nil {
		if kf.DNS.LocalIP == "" {
			kf.DNS.LocalIP = iputil.IPKey(dns.LocalIP)
		}
		if kf.DNS.RemoteIP == "" {
			kf.DNS.RemoteIP = iputil.IPKey(dns.RemoteIP)
		}
		kf.DNS.ExcludeSuffixes = append(kf.DNS.ExcludeSuffixes, dns.ExcludeSuffixes...)
		kf.DNS.IncludeSuffixes = append(kf.DNS.IncludeSuffixes, dns.IncludeSuffixes...)
		if kf.DNS.LookupTimeout.Duration == 0 {
			kf.DNS.LookupTimeout.Duration = dns.LookupTimeout
		}
	}
	if routing := remote.Routing; routing != nil {
		kf.AlsoProxy = append(kf.AlsoProxy, routing.AlsoProxy...)
		kf.NeverProxy = append(kf.NeverProxy, routing.NeverProxy...)
	}
	return nil
}
