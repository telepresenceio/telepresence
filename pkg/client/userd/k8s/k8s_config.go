package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	_ "k8s.io/client-go/plugin/pkg/client/auth" // Important for various cloud provider auth
	"k8s.io/client-go/rest"

	"github.com/datawire/dlib/dlog"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
)

type Config struct {
	client.KubeconfigExtension
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
		if err = json.Unmarshal(ext.Raw, &k.KubeconfigExtension); err != nil {
			return nil, errcat.Config.Newf("unable to parse extension %s in kubeconfig: %w", configExtension, err)
		}
	}

	if k.KubeconfigExtension.Manager == nil {
		k.KubeconfigExtension.Manager = &client.ManagerConfig{}
	}

	if k.KubeconfigExtension.Manager.Namespace == "" {
		k.KubeconfigExtension.Manager.Namespace = client.GetEnv(c).ManagerNamespace
	}

	return k, nil
}

// This represents an inClusterConfig.
func NewInClusterConfig(c context.Context, flagMap map[string]string) (*Config, error) {
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

	return &Config{
		Namespace:   namespace,
		Server:      restConfig.Host,
		flagMap:     flagMap,
		ConfigFlags: configFlags,
		RestConfig:  restConfig,
		// it may be empty, but we should avoid nil deref
		KubeconfigExtension: client.KubeconfigExtension{
			Manager: &client.ManagerConfig{
				Namespace: client.GetEnv(c).ManagerNamespace,
			},
		},
	}, nil
}

// ContextServiceAndFlagsEqual determines if this instance is equal to the given instance with respect to context,
// server, and flag arguments.
func (kf *Config) ContextServiceAndFlagsEqual(okf *Config) bool {
	return kf != nil && okf != nil &&
		kf.Context == okf.Context &&
		kf.Server == okf.Server &&
		maps.Equal(kf.flagMap, okf.flagMap)
}

func (kf *Config) GetManagerNamespace() string {
	return kf.KubeconfigExtension.Manager.Namespace
}

func (kf *Config) GetRestConfig() *rest.Config {
	return kf.RestConfig
}

func (kf *Config) AddRemoteKubeConfigExtension(ctx context.Context, cfgJson string) error {
	dlog.Debugf(ctx, "Applying remote kubeconfig: %s", cfgJson)
	remote := &client.KubeconfigExtension{}
	if err := json.Unmarshal([]byte(cfgJson), &remote); err != nil {
		return fmt.Errorf("unable to parse remote kubeconfig: %w", err)
	}
	if kf.DNS == nil {
		kf.DNS = remote.DNS
	} else {
		if kf.DNS.LocalIP == "" {
			kf.DNS.LocalIP = remote.DNS.LocalIP
		}
		if kf.DNS.RemoteIP == "" {
			kf.DNS.RemoteIP = remote.DNS.RemoteIP
		}
		kf.DNS.ExcludeSuffixes = append(kf.DNS.ExcludeSuffixes, remote.DNS.ExcludeSuffixes...)
		kf.DNS.IncludeSuffixes = append(kf.DNS.IncludeSuffixes, remote.DNS.IncludeSuffixes...)
		if kf.DNS.LookupTimeout.Duration == 0 {
			kf.DNS.LookupTimeout = remote.DNS.LookupTimeout
		}
	}
	kf.AlsoProxy = append(kf.AlsoProxy, remote.AlsoProxy...)
	kf.NeverProxy = append(kf.NeverProxy, remote.NeverProxy...)
	return nil
}
