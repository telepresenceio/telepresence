package client

import (
	"context"
	"encoding/csv"
	"net/netip"
	"os"
	"strings"

	"github.com/go-json-experiment/json"
	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/api/meta"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	_ "k8s.io/client-go/plugin/pkg/client/auth" // Important for various cloud provider auth
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
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

// The dnsConfig is part of the kubeconfigExtension struct.
// Deprecated: Use Config as the Kubeconfig extension.
type dnsConfig struct {
	// LocalIP is the address of the local DNS server. This entry is only
	// used on Linux system that are not configured to use systemd-resolved and
	// can be overridden by using the option --dns on the command line and defaults
	// to the first line of /etc/resolv.conf
	LocalIP netip.Addr `json:"local-ip,omitempty"`

	// RemoteIP is the address of the cluster's DNS service. It will default
	// to the IP of the kube-dns.kube-system or the dns-default.openshift-dns service.
	RemoteIP netip.Addr `json:"remote-ip,omitempty"`

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

// The managerConfig is part of the kubeconfigExtension struct. It configures discovery of the traffic manager.
// Deprecated: Use Config as the Kubeconfig extension.
type managerConfig struct {
	// Namespace is the name of the namespace where the traffic manager is to be found
	Namespace string `json:"namespace,omitempty"`
}

// kubeconfigExtension is an extension read from the selected kubeconfig Cluster.
// Deprecated: Use Config as the Kubeconfig extension.
type kubeconfigExtension struct {
	DNS                     *dnsConfig     `json:"dns,omitempty"`
	AlsoProxy               []netip.Prefix `json:"also-proxy,omitempty"`
	NeverProxy              []netip.Prefix `json:"never-proxy,omitempty"`
	AllowConflictingSubnets []netip.Prefix `json:"allow-conflicting-subnets,omitempty"`
	Manager                 *managerConfig `json:"manager,omitempty"`
}

func (ke *kubeconfigExtension) asConfig() Config {
	cfg := GetDefaultConfig()
	if keDns := ke.DNS; keDns != nil {
		dns := cfg.DNS()
		if len(keDns.Excludes) > 0 {
			dns.Excludes = keDns.Excludes
		}
		if len(keDns.ExcludeSuffixes) > 0 {
			dns.ExcludeSuffixes = keDns.ExcludeSuffixes
		}
		if len(keDns.IncludeSuffixes) > 0 {
			dns.IncludeSuffixes = keDns.IncludeSuffixes
		}
		if len(keDns.Mappings) > 0 {
			dns.Mappings = keDns.Mappings
		}
		if keDns.LocalIP.IsValid() {
			dns.LocalIP = keDns.LocalIP
		}
		if keDns.RemoteIP.IsValid() {
			dns.RemoteIP = keDns.RemoteIP
		}
		if keDns.LookupTimeout.Duration != 0 {
			dns.LookupTimeout = keDns.LookupTimeout.Duration
		}
	}
	rt := cfg.Routing()
	if len(ke.NeverProxy) > 0 {
		rt.NeverProxy = ke.NeverProxy
	}
	if len(ke.AlsoProxy) > 0 {
		rt.AlsoProxy = ke.AlsoProxy
	}
	if len(ke.AllowConflictingSubnets) > 0 {
		rt.AllowConflicting = ke.AllowConflictingSubnets
	}
	if m := ke.Manager; m != nil && m.Namespace != "" {
		cfg.Cluster().DefaultManagerNamespace = m.Namespace
	}
	return cfg
}

// Kubeconfig implements genericclioptions.RESTClientGetter, but is using the RestConfig
// instead of the ConfigFlags (which also implements that interface) since the latter
// will assume that the kubeconfig is loaded from disk.
type Kubeconfig struct {
	Namespace        string // default cluster namespace.
	Context          string
	Server           string
	OriginalFlagMap  map[string]string
	EffectiveFlagMap map[string]string
	ClientConfig     clientcmd.ClientConfig
	RestConfig       *rest.Config
}

func (kf *Kubeconfig) ToRESTConfig() (*rest.Config, error) {
	return kf.RestConfig, nil
}

func (kf *Kubeconfig) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(kf.RestConfig)
	if err != nil {
		return nil, err
	}
	return memory.NewMemCacheClient(discoveryClient), nil
}

func (kf *Kubeconfig) ToRESTMapper() (meta.RESTMapper, error) {
	discoveryClient, err := kf.ToDiscoveryClient()
	if err != nil {
		return nil, err
	}
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(discoveryClient)
	expander := restmapper.NewShortcutExpander(mapper, discoveryClient, func(string) {})
	return expander, nil
}

func (kf *Kubeconfig) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	return kf.ClientConfig
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
func ConfigLoader(ctx context.Context, flagMap map[string]string, kubeConfigData []byte) (clientcmd.ClientConfig, error) {
	configFlags, err := ConfigFlags(flagMap)
	if err != nil {
		return nil, err
	}
	return NewClientConfig(ctx, configFlags, kubeConfigData)
}

// CurrentContext returns the name of the current Kubernetes context, the active namespace, and the context itself.
func CurrentContext(ctx context.Context, flagMap map[string]string, configBytes []byte) (string, string, *api.Context, error) {
	cld, err := ConfigLoader(ctx, flagMap, configBytes)
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

func NewKubeconfig(c context.Context, flagMap map[string]string, managerNamespaceOverride string) (context.Context, *Kubeconfig, error) {
	configFlags, err := ConfigFlags(flagMap)
	if err != nil {
		return c, nil, err
	}
	return newKubeconfig(c, flagMap, flagMap, managerNamespaceOverride, configFlags, nil)
}

func DaemonKubeconfig(c context.Context, cr *connector.ConnectRequest) (context.Context, *Kubeconfig, error) {
	if cr.IsPodDaemon {
		ke, err := NewInClusterConfig(c, cr.KubeFlags)
		return c, ke, err
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
		return c, nil, err
	}
	return newKubeconfig(c, cr.KubeFlags, flagMap, cr.ManagerNamespace, configFlags, cr.KubeconfigData)
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

// flagOverrides creates overrides based on the given ConfigFlags.
//
// The code in this function is copied from clientcmd.config_flags.go, function toRawKubeConfigLoader
// but differs in that overrides are only made for non-zero values.
func flagOverrides(f *genericclioptions.ConfigFlags) *clientcmd.ConfigOverrides {
	overrides := &clientcmd.ConfigOverrides{ClusterDefaults: clientcmd.ClusterDefaults}

	stringVal := func(vp *string) (v string, ok bool) {
		if vp != nil && *vp != "" {
			v, ok = *vp, true
		}
		return
	}

	// bind auth info flag values to overrides
	if v, ok := stringVal(f.CertFile); ok {
		overrides.AuthInfo.ClientCertificate = v
	}
	if v, ok := stringVal(f.KeyFile); ok {
		overrides.AuthInfo.ClientKey = v
	}
	if v, ok := stringVal(f.BearerToken); ok {
		overrides.AuthInfo.Token = v
	}
	if v, ok := stringVal(f.Impersonate); ok {
		overrides.AuthInfo.Impersonate = v
	}
	if v, ok := stringVal(f.ImpersonateUID); ok {
		overrides.AuthInfo.ImpersonateUID = v
	}
	if f.ImpersonateGroup != nil && len(*f.ImpersonateGroup) > 0 {
		overrides.AuthInfo.ImpersonateGroups = *f.ImpersonateGroup
	}
	if v, ok := stringVal(f.Username); ok {
		overrides.AuthInfo.Username = v
	}
	if v, ok := stringVal(f.Password); ok {
		overrides.AuthInfo.Password = v
	}

	// bind cluster flags
	if v, ok := stringVal(f.APIServer); ok {
		overrides.ClusterInfo.Server = v
	}
	if v, ok := stringVal(f.TLSServerName); ok {
		overrides.ClusterInfo.TLSServerName = v
	}
	if v, ok := stringVal(f.CAFile); ok {
		overrides.ClusterInfo.CertificateAuthority = v
	}
	if f.Insecure != nil && *f.Insecure {
		overrides.ClusterInfo.InsecureSkipTLSVerify = true
	}
	if f.DisableCompression != nil && *f.DisableCompression {
		overrides.ClusterInfo.DisableCompression = true
	}

	// bind context flags
	if v, ok := stringVal(f.Context); ok {
		overrides.CurrentContext = v
	}
	if v, ok := stringVal(f.ClusterName); ok {
		overrides.Context.Cluster = v
	}
	if v, ok := stringVal(f.AuthInfoName); ok {
		overrides.Context.AuthInfo = v
	}
	if v, ok := stringVal(f.Namespace); ok {
		overrides.Context.Namespace = v
	}

	if v, ok := stringVal(f.Timeout); ok && v != "0" {
		overrides.Timeout = v
	}
	return overrides
}

type KubeconfigGetter func() (*api.Config, error)

type configGetter struct {
	kubeconfigGetter KubeconfigGetter
	destFile         string
}

func (g *configGetter) Load() (*api.Config, error) {
	return g.kubeconfigGetter()
}

func (g *configGetter) GetLoadingPrecedence() []string {
	return nil
}

func (g *configGetter) GetStartingConfig() (*api.Config, error) {
	return g.kubeconfigGetter()
}

func (g *configGetter) GetDefaultFilename() string {
	if g.destFile == "" {
		destFile, err := os.CreateTemp("", "kc-*")
		if err == nil {
			g.destFile = destFile.Name()
			_ = os.Remove(destFile.Name())
			_ = destFile.Close()
		}
	}
	return g.destFile
}

func (g *configGetter) IsExplicitFile() bool {
	return false
}

func (g *configGetter) GetExplicitFile() string {
	return ""
}

func (g *configGetter) IsDefaultConfig(config *rest.Config) bool {
	return false
}

// NewClientConfig creates a clientcmd.ClientConfig, by either reading the kubeconfig from the given configData or
// by loading it from files as configured by the given configFlags.
func NewClientConfig(ctx context.Context, configFlags *genericclioptions.ConfigFlags, configData []byte) (clientcmd.ClientConfig, error) {
	if len(configData) == 0 {
		return configFlags.ToRawKubeConfigLoader(), nil
	}
	directConfig, err := clientcmd.NewClientConfigFromBytes(configData)
	if err != nil {
		dlog.Errorf(ctx, "loading kubeconfig failed: %v", err)
		return nil, err
	}
	config, err := directConfig.RawConfig()
	if err != nil {
		dlog.Errorf(ctx, "raw kubeconfig failed: %v", err)
		return nil, err
	}
	overrides := flagOverrides(configFlags)
	currentContext := overrides.CurrentContext
	return clientcmd.NewNonInteractiveClientConfig(config, currentContext, overrides, &configGetter{
		kubeconfigGetter: func() (*api.Config, error) {
			return &config, nil
		},
	}), nil
}

func newKubeconfig(
	ctx context.Context,
	originalFlags,
	effectiveFlags map[string]string,
	managerNamespaceOverride string,
	configFlags *genericclioptions.ConfigFlags,
	configData []byte,
) (context.Context, *Kubeconfig, error) {
	clientConfig, err := NewClientConfig(ctx, configFlags, configData)
	if err != nil {
		return ctx, nil, err
	}

	config, err := clientConfig.RawConfig()
	if err != nil {
		return ctx, nil, err
	}
	if len(config.Contexts) == 0 {
		return ctx, nil, errcat.Config.New("kubeconfig has no context definition")
	}

	namespace, _, err := clientConfig.Namespace()
	if err != nil {
		return ctx, nil, err
	}

	ctxName := effectiveFlags["context"]
	if ctxName == "" {
		ctxName = config.CurrentContext
	}

	kubeCtx, ok := config.Contexts[ctxName]
	if !ok {
		return ctx, nil, errcat.Config.Newf("context %q does not exist in the kubeconfig", ctxName)
	}

	cluster, ok := config.Clusters[kubeCtx.Cluster]
	if !ok {
		return ctx, nil, errcat.Config.Newf("the cluster %q declared in context %q does exists in the kubeconfig", kubeCtx.Cluster, ctxName)
	}

	restConfig, err := clientConfig.ClientConfig()
	if err != nil {
		return ctx, nil, err
	}

	dlog.Debugf(ctx, "using namespace %q", namespace)

	k := &Kubeconfig{
		Context:          ctxName,
		Server:           cluster.Server,
		Namespace:        namespace,
		EffectiveFlagMap: effectiveFlags,
		OriginalFlagMap:  originalFlags,
		ClientConfig:     clientConfig,
		RestConfig:       restConfig,
	}

	cfg := GetConfig(ctx)
	managerNamespace := managerNamespaceOverride
	if managerNamespace == "" {
		managerNamespace = GetEnv(ctx).ManagerNamespace
	}

	var keCfg Config
	if ext, ok := cluster.Extensions[configExtension].(*runtime.Unknown); ok {
		if kc, err := UnmarshalJSONConfig(ext.Raw, true); err != nil {
			// Try with legacy kubeconfigExtension
			ke := kubeconfigExtension{}
			if keErr := json.Unmarshal(ext.Raw, &ke); keErr != nil {
				return ctx, nil, errcat.Config.Newf("unable to parse extension %s in kubeconfig: %w", configExtension, err)
			}
			keCfg = ke.asConfig()
		} else {
			keCfg = kc
		}
		if managerNamespace != "" {
			keCfg.Cluster().DefaultManagerNamespace = managerNamespace
		}
	} else if managerNamespace != "" && managerNamespace != cfg.Cluster().DefaultManagerNamespace {
		// No kubeconfig exists but we still need a config when the managerNamespace is set.
		keCfg = GetDefaultConfig()
		keCfg.Cluster().DefaultManagerNamespace = managerNamespace
	}
	if keCfg != nil {
		ctx = WithConfig(ctx, cfg.Merge(keCfg))
	}
	return ctx, k, nil
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

	return &Kubeconfig{
		Namespace:        namespace,
		Server:           restConfig.Host,
		EffectiveFlagMap: flagMap,
		OriginalFlagMap:  flagMap,
		RestConfig:       restConfig,
		ClientConfig:     configLoader,
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

func (kf *Kubeconfig) GetRestConfig() *rest.Config {
	return kf.RestConfig
}
