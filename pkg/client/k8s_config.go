package client

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/pflag"
	"gopkg.in/yaml.v3"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	_ "k8s.io/client-go/plugin/pkg/client/auth" // Important for various cloud provider auth
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

// DNSMapping contains a hostname and its associated alias. When requesting the name, the intended behavior is
// to resolve the alias instead.
type DNSMapping struct {
	Name     string `json:"name,omitempty" yaml:"name,omitempty"`
	AliasFor string `json:"aliasFor,omitempty" yaml:"aliasFor,omitempty"`
}

type DNSMappings []*DNSMapping

func (d *DNSMappings) FromRPC(rpcMappings []*rpc.DNSMapping) {
	*d = make(DNSMappings, 0, len(rpcMappings))
	for i := range rpcMappings {
		*d = append(*d, &DNSMapping{
			Name:     rpcMappings[i].Name,
			AliasFor: rpcMappings[i].AliasFor,
		})
	}
}

func (d DNSMappings) ToRPC() []*rpc.DNSMapping {
	rpcMappings := make([]*rpc.DNSMapping, 0, len(d))
	for i := range d {
		rpcMappings = append(rpcMappings, &rpc.DNSMapping{
			Name:     d[i].Name,
			AliasFor: d[i].AliasFor,
		})
	}
	return rpcMappings
}

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

	// Excludes are a list of hostname that the DNS resolver will not resolve even if they exist.
	Excludes []string `json:"excludes,omitempty"`

	// Mappings contains a list of DNS Mappings. Each item references a hostname, and an associated alias. If a
	// request is made for the name, the alias will be resolved instead.
	Mappings DNSMappings `json:"mappings,omitempty"`

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
	DNS                     *DnsConfig       `json:"dns,omitempty"`
	AlsoProxy               []*iputil.Subnet `json:"also-proxy,omitempty"`
	NeverProxy              []*iputil.Subnet `json:"never-proxy,omitempty"`
	AllowConflictingSubnets []*iputil.Subnet `json:"allow-conflicting-subnets,omitempty"`
	Manager                 *ManagerConfig   `json:"manager,omitempty"`
}

type Kubeconfig struct {
	KubeconfigExtension
	Namespace        string // default cluster namespace.
	Context          string
	Server           string
	OriginalFlagMap  map[string]string
	EffectiveFlagMap map[string]string
	ConfigFlags      *genericclioptions.ConfigFlags
	RestConfig       *rest.Config
}

const configExtension = "telepresence.io"

func ConfigFlags(flagMap map[string]string) (*genericclioptions.ConfigFlags, error) {
	configFlags := genericclioptions.NewConfigFlags(false)
	flags := pflag.NewFlagSet("", 0)
	configFlags.AddFlags(flags)
	for k, v := range flagMap {
		f := flags.Lookup(k)
		if f == nil {
			continue
		}
		var err error
		if sv, ok := f.Value.(pflag.SliceValue); ok {
			var vs []string
			if vs, err = csv.NewReader(strings.NewReader(v)).Read(); err == nil {
				err = sv.Replace(vs)
			}
		} else {
			err = flags.Set(k, v)
		}
		if err != nil {
			return nil, errcat.User.Newf("error processing kubectl flag --%s=%s: %w", k, v, err)
		}
	}
	return configFlags, nil
}

// ConfigLoader returns the name of the current Kubernetes context, and the context itself.
func ConfigLoader(flagMap map[string]string) (clientcmd.ClientConfig, error) {
	configFlags, err := ConfigFlags(flagMap)
	if err != nil {
		return nil, err
	}
	return configFlags.ToRawKubeConfigLoader(), nil
}

// CurrentContext returns the name of the current Kubernetes context, the active namespace, and the context itself.
func CurrentContext(flagMap map[string]string) (string, string, *api.Context, error) {
	cld, err := ConfigLoader(flagMap)
	if err != nil {
		return "", "", nil, err
	}
	ns, _, err := cld.Namespace()
	if err != nil {
		return "", "", nil, err
	}

	config, err := cld.RawConfig()
	if err != nil {
		return "", "", nil, err
	}
	if len(config.Contexts) == 0 {
		return "", "", nil, errcat.Config.New("kubeconfig has no context definition")
	}
	cc := flagMap["context"]
	if cc == "" {
		cc = config.CurrentContext
	}
	return cc, ns, config.Contexts[cc], nil
}

func NewKubeconfig(c context.Context, flagMap map[string]string, managerNamespaceOverride string) (*Kubeconfig, error) {
	configFlags, err := ConfigFlags(flagMap)
	if err != nil {
		return nil, err
	}
	return newKubeconfig(c, flagMap, flagMap, managerNamespaceOverride, configFlags)
}

func DaemonKubeconfig(c context.Context, cr *connector.ConnectRequest) (*Kubeconfig, error) {
	if cr.IsPodDaemon {
		return NewInClusterConfig(c, cr.KubeFlags)
	}
	flagMap := cr.KubeFlags
	if proc.RunningInContainer() {
		// Don't trust the host's KUBECONFIG env.
		delete(cr.Environment, "KUBECONFIG")

		// Add potential overrides for kube flags.
		if len(cr.ContainerKubeFlagOverrides) > 0 {
			flagMap = maps.Copy(flagMap)
			maps.Merge(flagMap, cr.ContainerKubeFlagOverrides)
		}
	}
	for k, v := range cr.Environment {
		if k[0] == '-' {
			_ = os.Unsetenv(k[1:])
		} else {
			_ = os.Setenv(k, v)
		}
	}
	configFlags, err := ConfigFlags(flagMap)
	if err != nil {
		return nil, err
	}
	return newKubeconfig(c, cr.KubeFlags, flagMap, cr.ManagerNamespace, configFlags)
}

// AppendKubeFlags appends the flags in the given map to the given slice in the form of
// flag arguments suitable for command execution. Flags known to be multivalued are assumed
// to be in the form of comma-separated list and will be added using repeated options.
func AppendKubeFlags(kubeFlags map[string]string, args []string) ([]string, error) {
	for k, v := range kubeFlags {
		switch k {
		case "as-group":
			// Multivalued
			r := csv.NewReader(strings.NewReader(v))
			gs, err := r.Read()
			if err != nil {
				return nil, err
			}
			for _, g := range gs {
				args = append(args, "--"+k, g)
			}
		case "disable-compression", "insecure-skip-tls-verify":
			// Boolean with false default.
			if v != "false" {
				args = append(args, "--"+k)
			}
		default:
			args = append(args, "--"+k, v)
		}
	}
	return args, nil
}

func newKubeconfig(
	ctx context.Context,
	originalFlags,
	effectiveFlags map[string]string,
	managerNamespaceOverride string,
	configFlags *genericclioptions.ConfigFlags,
) (*Kubeconfig, error) {
	configLoader := configFlags.ToRawKubeConfigLoader()
	config, err := configLoader.RawConfig()
	if err != nil {
		return nil, err
	}

	if len(config.Contexts) == 0 {
		return nil, errcat.Config.New("kubeconfig has no context definition")
	}

	namespace, _, err := configLoader.Namespace()
	if err != nil {
		return nil, err
	}

	ctxName := effectiveFlags["context"]
	if ctxName == "" {
		ctxName = config.CurrentContext
	}

	kubeCtx, ok := config.Contexts[ctxName]
	if !ok {
		return nil, errcat.Config.Newf("context %q does not exist in the kubeconfig", ctxName)
	}

	cluster, ok := config.Clusters[kubeCtx.Cluster]
	if !ok {
		return nil, errcat.Config.Newf("the cluster %q declared in context %q does exists in the kubeconfig", kubeCtx.Cluster, ctxName)
	}

	restConfig, err := configLoader.ClientConfig()
	if err != nil {
		return nil, err
	}

	dlog.Debugf(ctx, "using namespace %q", namespace)

	k := &Kubeconfig{
		Context:          ctxName,
		Server:           cluster.Server,
		Namespace:        namespace,
		EffectiveFlagMap: effectiveFlags,
		OriginalFlagMap:  originalFlags,
		ConfigFlags:      configFlags,
		RestConfig:       restConfig,
	}

	if ext, ok := cluster.Extensions[configExtension].(*runtime.Unknown); ok {
		if err = json.Unmarshal(ext.Raw, &k.KubeconfigExtension); err != nil {
			return nil, errcat.Config.Newf("unable to parse extension %s in kubeconfig: %w", configExtension, err)
		}
	}

	if k.KubeconfigExtension.Manager == nil {
		k.KubeconfigExtension.Manager = &ManagerConfig{}
	}

	if managerNamespaceOverride != "" {
		k.KubeconfigExtension.Manager.Namespace = managerNamespaceOverride
	}

	if k.KubeconfigExtension.Manager.Namespace == "" {
		k.KubeconfigExtension.Manager.Namespace = GetEnv(ctx).ManagerNamespace
	}
	if k.KubeconfigExtension.Manager.Namespace == "" {
		k.KubeconfigExtension.Manager.Namespace = GetConfig(ctx).Cluster().DefaultManagerNamespace
	}
	return k, nil
}

// NewInClusterConfig represents an inClusterConfig.
func NewInClusterConfig(c context.Context, flagMap map[string]string) (*Kubeconfig, error) {
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

	namespace, _, err := configLoader.Namespace()
	if err != nil {
		return nil, err
	}

	managerNamespace := GetEnv(c).ManagerNamespace
	if managerNamespace == "" {
		managerNamespace = GetConfig(c).Cluster().DefaultManagerNamespace
	}

	return &Kubeconfig{
		Namespace:        namespace,
		Server:           restConfig.Host,
		EffectiveFlagMap: flagMap,
		OriginalFlagMap:  flagMap,
		ConfigFlags:      configFlags,
		RestConfig:       restConfig,
		// it may be empty, but we should avoid nil deref
		KubeconfigExtension: KubeconfigExtension{
			Manager: &ManagerConfig{
				Namespace: managerNamespace,
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
		maps.Equal(kf.EffectiveFlagMap, okf.EffectiveFlagMap)
}

func (kf *Kubeconfig) GetContext() string {
	return kf.Context
}

func (kf *Kubeconfig) GetManagerNamespace() string {
	return kf.KubeconfigExtension.Manager.Namespace
}

func (kf *Kubeconfig) GetRestConfig() *rest.Config {
	return kf.RestConfig
}

func (kf *Kubeconfig) AddRemoteKubeConfigExtension(ctx context.Context, cfgYaml []byte) error {
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
			dlog.Debugf(ctx, "Applying remote dns local IP: %s", dns.LocalIP)
		}
		if kf.DNS.RemoteIP == "" {
			kf.DNS.RemoteIP = iputil.IPKey(dns.RemoteIP)
			dlog.Debugf(ctx, "Applying remote dns remote IP: %s", dns.RemoteIP)
		}
		if len(dns.ExcludeSuffixes) > 0 {
			dlog.Debugf(ctx, "Applying remote excludeSuffixes: %v", dns.ExcludeSuffixes)
			kf.DNS.ExcludeSuffixes = append(kf.DNS.ExcludeSuffixes, dns.ExcludeSuffixes...)
		}
		if len(dns.IncludeSuffixes) > 0 {
			dlog.Debugf(ctx, "Applying remote includeSuffixes: %v", dns.IncludeSuffixes)
			kf.DNS.IncludeSuffixes = append(kf.DNS.IncludeSuffixes, dns.IncludeSuffixes...)
		}
		if len(dns.Excludes) > 0 {
			dlog.Debugf(ctx, "Applying remote excludes: %v", dns.Excludes)
			kf.DNS.Excludes = append(kf.DNS.Excludes, dns.Excludes...)
		}
		if len(dns.Mappings) > 0 {
			for _, m := range dns.Mappings {
				dlog.Debugf(ctx, "Applying remote mapping: Name: %s, AliasFor %s", m.Name, m.AliasFor)
			}
			kf.DNS.Mappings = append(kf.DNS.Mappings, dns.Mappings...)
		}

		if kf.DNS.LookupTimeout.Duration == 0 {
			dlog.Debugf(ctx, "Applying remote lookupTimeout: %s", dns.LookupTimeout)
			kf.DNS.LookupTimeout.Duration = dns.LookupTimeout
		}
	}
	if routing := remote.Routing; routing != nil {
		if len(routing.AlsoProxy) > 0 {
			dlog.Debugf(ctx, "Applying remote alsoProxy: %v", routing.AlsoProxy)
			kf.AlsoProxy = append(kf.AlsoProxy, routing.AlsoProxy...)
		}
		if len(routing.NeverProxy) > 0 {
			dlog.Debugf(ctx, "Applying remote neverProxy: %v", routing.NeverProxy)
			kf.NeverProxy = append(kf.NeverProxy, routing.NeverProxy...)
		}
	}
	return nil
}
