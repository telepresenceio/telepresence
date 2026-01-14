package patcher

import (
	"context"
	"fmt"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/telepresenceio/dlib/v2/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/global"
)

const (
	kubeConfigStubSubCommands = "kubeauth"
)

// AddressProvider is a function that returns the path to the telepresence executable and an address to a service that
// implements the Authenticator gRPC.
//
// The function will typically start the gRPC service, and the service is therefore given
// a list of files that it must listen to in order to reliably resolve requests. It is
// also passed a pointer to the minified config that will be stored in a file so that it
// has a chance to modify it.
type (
	AddressProvider func(configFiles []string) (executable, addr, configFile string, err error)
	Patcher         func(*clientcmdapi.Config) error
)

// CreateExternalKubeConfig will load the current kubeconfig and minimize it so that it just contains the current
// context. Exec configs in that context are replaced by a stub binary that calls the kubeauth service. The kubeauth
// service, which runs on the host with the user's credentials, will then use the original Exec config.
// The minified config is stored in the <telepresence cache>/kube directory and returned.
func CreateExternalKubeConfig(
	ctx context.Context,
	loader clientcmd.ClientConfig,
	kubeContext string,
	authAddressFunc AddressProvider,
	patcher Patcher,
) ([]byte, error) {
	ns, _, err := loader.Namespace()
	if err != nil {
		return nil, err
	}

	configFiles := loader.ConfigAccess().GetLoadingPrecedence()
	dlog.Debugf(ctx, "host kubeconfig = %v", configFiles)
	origConfig, err := loader.RawConfig()
	if err != nil {
		return nil, err
	}
	var config clientcmdapi.Config
	origConfig.DeepCopyInto(&config)

	// Minify the config so that we only deal with the current context.
	if kubeContext != "" {
		config.CurrentContext = kubeContext
	}
	if err = clientcmdapi.MinifyConfig(&config); err != nil {
		return nil, err
	}
	dlog.Debugf(ctx, "context = %q, namespace %q", config.CurrentContext, ns)

	// Minify guarantees that the CurrentContext is set, but not that it has a cluster
	cc := config.Contexts[config.CurrentContext]
	if cc.Cluster == "" {
		return nil, fmt.Errorf("current context %q has no cluster", config.CurrentContext)
	}

	if needsStubbedExec(&config) {
		executable, addr, configFile, err := authAddressFunc(configFiles)
		if err != nil {
			return nil, err
		}
		if err = replaceAuthExecWithStub(&config, executable, addr, configFile); err != nil {
			return nil, err
		}
	}

	// Ensure that all certs are embedded instead of reachable using a path
	if err = clientcmdapi.FlattenConfig(&config); err != nil {
		return nil, err
	}

	if patcher != nil {
		if err = patcher(&config); err != nil {
			return nil, err
		}
	}
	return clientcmd.Write(config)
}

// replaceAuthExecWithStub goes through the kubeconfig and replaces all uses of the Exec auth method by
// an invocation of the stub binary.
func replaceAuthExecWithStub(rawConfig *clientcmdapi.Config, executable, address, configFile string) error {
	for contextName, kubeContext := range rawConfig.Contexts {
		// Find related Auth.
		authInfo, ok := rawConfig.AuthInfos[kubeContext.AuthInfo]
		if !ok {
			return fmt.Errorf("auth info %s not found for context %s", kubeContext.AuthInfo, contextName)
		}

		// If it isn't an exec mode context, just return the default host kubeconfig.
		if authInfo.Exec == nil {
			continue
		}

		// Patch exec.
		authInfo.Exec = &clientcmdapi.ExecConfig{
			InteractiveMode: clientcmdapi.NeverExecInteractiveMode,
			APIVersion:      authInfo.Exec.APIVersion,
			Command:         executable,
			Args:            []string{kubeConfigStubSubCommands, "--" + global.FlagConfig, configFile, contextName, address},
		}
	}
	return nil
}

// needsStubbedExec returns true if the config contains at least one user with an Exec type AuthInfo.
func needsStubbedExec(rawConfig *clientcmdapi.Config) bool {
	for _, kubeContext := range rawConfig.Contexts {
		if authInfo, ok := rawConfig.AuthInfos[kubeContext.AuthInfo]; ok && authInfo.Exec != nil {
			return true
		}
	}
	return false
}
