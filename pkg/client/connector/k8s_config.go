package connector

import (
	"errors"
	"fmt"
	"sort"

	"github.com/spf13/pflag"
	"k8s.io/client-go/rest"

	"github.com/datawire/ambassador/pkg/kates"
)

type k8sConfig struct {
	Namespace   string // default cluster namespace.
	Context     string
	Server      string
	flagMap     map[string]string
	flagArgs    []string
	configFlags *kates.ConfigFlags
	config      *rest.Config
}

func newK8sConfig(flagMap map[string]string) (*k8sConfig, error) {
	// Namespace option will be passed only when explicitly needed. The k8Cluster is namespace agnostic with
	// respect to this option.
	delete(flagMap, "namespace")

	flagArgs := make([]string, 0, len(flagMap))
	configFlags := kates.NewConfigFlags(false)
	flags := pflag.NewFlagSet("", 0)
	configFlags.AddFlags(flags)
	for k, v := range flagMap {
		flagArgs = append(flagArgs, "--"+k+"="+v)
		if err := flags.Set(k, v); err != nil {
			return nil, fmt.Errorf("error processing kubectl flag --%s=%s: %v", k, v, err)
		}
	}

	configLoader := configFlags.ToRawKubeConfigLoader()
	config, err := configLoader.RawConfig()
	if err != nil {
		return nil, err
	}

	if len(config.Contexts) == 0 {
		return nil, errors.New("kubeconfig has no context definition")
	}

	ctxName := flagMap["context"]
	if ctxName == "" {
		ctxName = config.CurrentContext
	}

	ctx, ok := config.Contexts[ctxName]
	if !ok {
		return nil, fmt.Errorf("context %q does not exist in the kubeconfig", ctxName)
	}

	cluster, ok := config.Clusters[ctx.Cluster]
	if !ok {
		return nil, fmt.Errorf("cluster %q but no entry for that cluster exists in the kubeconfig", ctx.Cluster)
	}

	restConfig, err := configLoader.ClientConfig()
	if err != nil {
		return nil, err
	}

	namespace := ctx.Namespace
	if namespace == "" {
		namespace = "default"
	}

	// Sort for easy comparison
	sort.Strings(flagArgs)

	return &k8sConfig{
		Context:     ctxName,
		Server:      cluster.Server,
		Namespace:   namespace,
		flagMap:     flagMap,
		flagArgs:    flagArgs,
		configFlags: configFlags,
		config:      restConfig,
	}, nil
}

func (kc *k8sConfig) actualNamespace(namespace string) string {
	if namespace == "" {
		namespace = kc.Namespace
	}
	return namespace
}

// equals determines if this instance is equal to the given instance with respect to everything but
// Namespace.
func (kf *k8sConfig) equals(okf *k8sConfig) bool {
	return kf != nil && okf != nil &&
		kf.Context == okf.Context &&
		kf.Server == okf.Server &&
		sliceEqual(kf.flagArgs, okf.flagArgs)
}

func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
