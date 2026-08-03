package rt

import (
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"

	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
)

// pinContextKubeconfig makes RTEST_CONTEXT reach the telepresence child
// processes, which take their context from KUBECONFIG's current-context
// rather than from the --context flag the kubectl invocations get. When the
// run's kubeconfig has a different current-context, a flattened copy with
// RTEST_CONTEXT as current-context is written under the artifact dir and
// becomes the run's kubeconfig, so childEnv passes it to every child.
func (r *Runtime) pinContextKubeconfig() error {
	if r.kubeCtx == "" {
		return nil
	}
	cfg, err := loadRunKubeConfig(r)
	if err != nil {
		return fmt.Errorf("rtest: loading kubeconfig: %w", err)
	}
	if _, ok := cfg.Contexts[r.kubeCtx]; !ok {
		return fmt.Errorf("rtest: RTEST_CONTEXT %q not found in kubeconfig", r.kubeCtx)
	}
	if cfg.CurrentContext == r.kubeCtx {
		return nil
	}
	cfg.CurrentContext = r.kubeCtx
	path := filepath.Join(r.artifactDir, "kubeconfig-pinned.yaml")
	if err := clientcmd.WriteToFile(*cfg, path); err != nil {
		return fmt.Errorf("rtest: writing %s: %w", path, err)
	}
	r.kubeconfig = path
	return nil
}

// KubeConfigCopy loads the run's kubeconfig, applies mutate to an in-memory
// copy of it, writes the result under ArtifactDir("kubeconfig"), and returns
// its path. Callers pass the path to ConnWithKubeconfig; connect has no
// --kubeconfig flag, so pointing a connection at a derived kubeconfig is
// done through that connection's KUBECONFIG env var instead.
func KubeConfigCopy(e Env, mutate func(*api.Config)) (string, error) {
	e.T.Helper()
	cfg, err := loadRunKubeConfig(e.R)
	if err != nil {
		return "", fmt.Errorf("rtest: loading kubeconfig: %w", err)
	}
	mutate(cfg)
	data, err := clientcmd.Write(*cfg)
	if err != nil {
		return "", fmt.Errorf("rtest: marshaling kubeconfig: %w", err)
	}
	path := filepath.Join(e.R.ArtifactDir("kubeconfig"), sha256Hex(data)[:16]+".yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("rtest: writing %s: %w", path, err)
	}
	return path, nil
}

// WithKubeConfigExtension is KubeConfigCopy with mutate adding ext as the
// "telepresence.io" extension object on the current context's cluster entry:
// the client accepts also-proxy, never-proxy, dns.include-suffixes/
// exclude-suffixes, and manager.namespace keys there (see
// pkg/client/k8s/config.go's kubeconfigExtension).
func WithKubeConfigExtension(e Env, ext map[string]any) (string, error) {
	e.T.Helper()
	raw, err := json.Marshal(ext)
	if err != nil {
		return "", fmt.Errorf("rtest: marshaling kubeconfig extension: %w", err)
	}
	return KubeConfigCopy(e, func(cfg *api.Config) {
		cc := cfg.Contexts[cfg.CurrentContext]
		if cc == nil {
			return
		}
		cluster := cfg.Clusters[cc.Cluster]
		if cluster == nil {
			return
		}
		if cluster.Extensions == nil {
			cluster.Extensions = map[string]k8sruntime.Object{}
		}
		cluster.Extensions["telepresence.io"] = &k8sruntime.Unknown{Raw: raw}
	})
}

// loadRunKubeConfig loads the kubeconfig this run targets: RTEST_KUBECONFIG/
// KUBECONFIG when set, else the default client-go loading rules (~/.kube/
// config and friends).
func loadRunKubeConfig(r *Runtime) (*api.Config, error) {
	path := r.kubeconfig
	if path == "" {
		path = os.Getenv("KUBECONFIG")
	}
	if path != "" {
		return clientcmd.LoadFromFile(path)
	}
	return clientcmd.NewDefaultClientConfigLoadingRules().Load()
}
