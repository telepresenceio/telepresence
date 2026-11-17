package patcher

import (
	"context"
	"fmt"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/global"
)

const (
	kubeConfigStubSubCommands = "kubeauth"
)

// AddressProvider is a function that returns the path to the telepresence executable, an address to a service that
// implements the Authenticator gRPC, and the token that the service requires (empty when it requires none).
//
// The function will typically start the gRPC service, and the service is therefore given
// a list of files that it must listen to in order to reliably resolve requests. It is
// also passed a pointer to the minified config that will be stored in a file so that it
// has a chance to modify it.
type (
	AddressProvider func(configFiles []string) (executable, addr, configFile, token string, err error)
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
	config, err := minifiedConfig(ctx, loader, kubeContext)
	if err != nil {
		return nil, err
	}

	configFiles := loader.ConfigAccess().GetLoadingPrecedence()

	if needsStubbedExec(&config) {
		executable, addr, configFile, token, err := authAddressFunc(configFiles)
		if err != nil {
			return nil, err
		}
		if err = replaceAuthExecWithStub(&config, executable, addr, configFile, token); err != nil {
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
func replaceAuthExecWithStub(rawConfig *clientcmdapi.Config, executable, address, configFile, token string) error {
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
		args := []string{kubeConfigStubSubCommands, "--" + global.FlagConfig, configFile}
		if token != "" {
			args = append(args, "--token", token)
		}
		authInfo.Exec = &clientcmdapi.ExecConfig{
			InteractiveMode: clientcmdapi.NeverExecInteractiveMode,
			APIVersion:      authInfo.Exec.APIVersion,
			Command:         executable,
			Args:            append(args, contextName, address),
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

// CreateKubeConfigWithoutExec will load the current kubeconfig, minimize it so that it just contains the current
// context, and flatten it. The Exec config of the context's user is removed from the result and returned
// separately. It is nil when the user has none.
func CreateKubeConfigWithoutExec(ctx context.Context, loader clientcmd.ClientConfig, kubeContext string) ([]byte, *clientcmdapi.ExecConfig, error) {
	config, err := minifiedConfig(ctx, loader, kubeContext)
	if err != nil {
		return nil, nil, err
	}
	var execConfig *clientcmdapi.ExecConfig
	if authInfo, ok := config.AuthInfos[config.Contexts[config.CurrentContext].AuthInfo]; ok && authInfo.Exec != nil {
		execConfig = authInfo.Exec.DeepCopy()
		authInfo.Exec = nil
	}
	if err = clientcmdapi.FlattenConfig(&config); err != nil {
		return nil, nil, err
	}
	data, err := clientcmd.Write(config)
	if err != nil {
		return nil, nil, err
	}
	return data, execConfig, nil
}

// minifiedConfig returns a copy of the loader's config that just contains the current (or given) context.
func minifiedConfig(ctx context.Context, loader clientcmd.ClientConfig, kubeContext string) (clientcmdapi.Config, error) {
	var config clientcmdapi.Config
	ns, _, err := loader.Namespace()
	if err != nil {
		return config, err
	}

	configFiles := loader.ConfigAccess().GetLoadingPrecedence()
	clog.Debugf(ctx, "host kubeconfig = %v", configFiles)
	origConfig, err := loader.RawConfig()
	if err != nil {
		return config, err
	}
	origConfig.DeepCopyInto(&config)

	// Minify the config so that we only deal with the current context.
	if kubeContext != "" {
		config.CurrentContext = kubeContext
	}
	if err = clientcmdapi.MinifyConfig(&config); err != nil {
		return config, err
	}
	clog.Debugf(ctx, "context = %q, namespace %q", config.CurrentContext, ns)

	// Minify guarantees that the CurrentContext is set, but not that it has a cluster
	cc := config.Contexts[config.CurrentContext]
	if cc.Cluster == "" {
		return config, fmt.Errorf("current context %q has no cluster", config.CurrentContext)
	}
	return config, nil
}
