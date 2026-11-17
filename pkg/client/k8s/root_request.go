package k8s

import (
	"fmt"
	"slices"
	"sort"

	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

// rootKubeFlags are the kubectl flags that the root daemon accepts. Every other flag is
// expressed in the kubeconfig that the user daemon sends.
var rootKubeFlags = []string{ //nolint:gochecknoglobals // constant
	// The request timeout is not part of a kubeconfig.
	"request-timeout",
}

// RootKubeFlags returns the subset of the given kubectl flags that the root daemon accepts.
func RootKubeFlags(flags map[string]string) map[string]string {
	var rf map[string]string
	for _, name := range rootKubeFlags {
		if v, ok := flags[name]; ok {
			if rf == nil {
				rf = make(map[string]string, len(rootKubeFlags))
			}
			rf[name] = v
		}
	}
	return rf
}

// CheckRootRequest verifies that the kubectl flags and the kubeconfig of a root daemon
// connect request name no file, program or in-cluster setting. The request must carry a
// complete kubeconfig in which every certificate, key, and token is inline, and the only
// flags that it may carry are those returned by RootKubeFlags.
//
// The messages name what was refused but never show a value.
func CheckRootRequest(flags map[string]string, kubeconfigData []byte) error {
	names := make([]string, 0, len(flags))
	for name := range flags {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !slices.Contains(rootKubeFlags, name) {
			return errcat.Config.Newf("the root daemon does not accept the kubectl flag %q", name)
		}
		if _, err := clientcmd.ParseTimeout(flags[name]); err != nil {
			return errcat.Config.Newf("the value of the kubectl flag %q is invalid", name)
		}
	}
	if len(kubeconfigData) == 0 {
		return errcat.Config.New("the request carries no kubeconfig")
	}
	config, err := clientcmd.Load(kubeconfigData)
	if err != nil {
		return errcat.Config.New("the kubeconfig in the request cannot be parsed")
	}
	return checkRootKubeconfig(config)
}

func checkRootKubeconfig(config *api.Config) error {
	for _, name := range sortedKeys(config.AuthInfos) {
		ai := config.AuthInfos[name]
		if ai == nil {
			continue
		}
		var what string
		switch {
		case ai.Exec != nil:
			what = "a credential plugin"
		case ai.AuthProvider != nil:
			what = "an auth provider"
		case ai.TokenFile != "":
			what = "a token file"
		case ai.ClientCertificate != "":
			what = "a client certificate file"
		case ai.ClientKey != "":
			what = "a client key file"
		}
		if what != "" {
			return errcat.Config.Newf("the kubeconfig user %q refers to %s, which the root daemon does not accept", name, what)
		}
	}
	for _, name := range sortedKeys(config.Clusters) {
		if cl := config.Clusters[name]; cl != nil && cl.CertificateAuthority != "" {
			return errcat.Config.Newf("the kubeconfig cluster %q refers to a certificate authority file, which the root daemon does not accept", name)
		}
	}

	// A cluster without a server makes client-go use a default server, and in some cases the in-cluster
	// configuration of the process, so the cluster of the current context must name one.
	if config.CurrentContext == "" {
		return errcat.Config.New("the kubeconfig has no current context")
	}
	kc, ok := config.Contexts[config.CurrentContext]
	if !ok || kc == nil {
		return errcat.Config.Newf("the current context %q does not exist in the kubeconfig", config.CurrentContext)
	}
	cl, ok := config.Clusters[kc.Cluster]
	if !ok || cl == nil {
		return errcat.Config.Newf("the cluster %q of context %q does not exist in the kubeconfig", kc.Cluster, config.CurrentContext)
	}
	if cl.Server == "" {
		return errcat.Config.Newf("the cluster %q in the kubeconfig has no server", kc.Cluster)
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ManagerServerCAPEM returns the PEM text that a cluster.managerServerCA setting stands for.
// The setting can be a PEM block, the path to a file that contains one, or a base64-encoded
// PEM block.
func ManagerServerCAPEM(spec string) (string, error) {
	if spec == "" {
		return "", nil
	}
	pem, err := resolveManagerServerCA(spec)
	if err != nil {
		return "", fmt.Errorf("unable to resolve cluster.managerServerCA: %w", err)
	}
	return string(pem), nil
}
