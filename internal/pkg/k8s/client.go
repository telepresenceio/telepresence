package k8s

import (
	"strings"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// KubeInfo holds the data required to talk to a cluster
type KubeInfo struct {
	Kubeconfig   string
	Context      string
	Namespace    string
	clientConfig clientcmd.ClientConfig
}

// NewKubeInfo returns a useable KubeInfo, handling optional
// kubeconfig, context, and namespace.
func NewKubeInfo(configfile, context, namespace string) (*KubeInfo, error) {
	// Find the correct kube config file
	configfilesearch := clientcmd.NewDefaultClientConfigLoadingRules()
	if len(configfile) != 0 {
		configfilesearch.ExplicitPath = configfile
	}

	// Possibly override context and namespace
	overrides := &clientcmd.ConfigOverrides{}
	if len(context) != 0 {
		overrides.CurrentContext = context
	}
	if len(namespace) != 0 {
		overrides.Context.Namespace = namespace
	}

	// Construct the config
	kubeconfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(configfilesearch, overrides)

	// Extract resulting context and namespace
	resultContext := context
	if len(context) == 0 {
		apiconfig, err := kubeconfig.RawConfig()
		if err != nil {
			return nil, err
		}
		resultContext = apiconfig.CurrentContext
	}

	resultNamespace, _, err := kubeconfig.Namespace()
	if err != nil {
		return nil, err
	}

	res := KubeInfo{
		configfile,
		resultContext,
		resultNamespace,
		kubeconfig,
	}

	return &res, nil
}

// GetRestConfig returns a REST config
func (info *KubeInfo) GetRestConfig() (*rest.Config, error) {
	/*
		// Do the right thing if you're running in a cluster
		if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
			inClusterConfig, err := rest.InClusterConfig()
			if err != nil {
				return nil, err
			}
			return inClusterConfig, nil
		}
	*/

	config, err := info.clientConfig.ClientConfig()
	if err != nil {
		return nil, err
	}
	return config, nil
}

// GetKubectl returns the arguents for a runnable kubectl command
func (info *KubeInfo) GetKubectl(args string) string {
	res := []string{"kubectl"}
	if len(info.Kubeconfig) != 0 {
		res = append(res, "--kubeconfig", info.Kubeconfig)
	}
	res = append(res, "--context", info.Context, "--namespace", info.Namespace)
	res = append(res, args)
	return strings.Join(res[1:], " ") // Drop leading "kubectl" because reasons...
}
