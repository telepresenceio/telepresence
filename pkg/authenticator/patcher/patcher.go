package patcher

import (
	"context"
	"fmt"
	"path/filepath"

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
	origConfig, err := loader.RawConfig()
	if err != nil {
		return nil, err
	}
	configFiles := loader.ConfigAccess().GetLoadingPrecedence()
	clog.Debugf(ctx, "host kubeconfig = %v", configFiles)
	config, err := minifiedConfig(ctx, origConfig, kubeContext)
	if err != nil {
		return nil, err
	}

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

// RootKubeConfig is a kubeconfig for the root daemon.
type RootKubeConfig struct {
	// Data is the kubeconfig. It names no file and no credential plugin.
	Data []byte

	// Exec is the credential plugin that the user of the context has, or nil.
	Exec *clientcmdapi.ExecConfig

	// TokenFile is the token file that the user of the context has. It is empty when the user has a
	// credential plugin or no token file.
	TokenFile string
}

// CreateRootKubeConfig returns a kubeconfig that the root daemon can use as is. It is the kubeconfig that the
// loader results in once all overrides are applied, minimized so that it just contains the current (or
// given) context, and flattened so that all certificates and keys are inline. The credential plugin and the
// token file of the context's user are removed from the result and returned separately, because the
// root daemon doesn't run programs and doesn't read files.
func CreateRootKubeConfig(ctx context.Context, loader clientcmd.OverridingClientConfig, kubeContext string) (*RootKubeConfig, error) {
	merged, err := loader.MergedRawConfig()
	if err != nil {
		return nil, err
	}
	nameAnonymousUser(&merged, kubeContext)
	config, err := minifiedConfig(ctx, merged, kubeContext)
	if err != nil {
		return nil, err
	}
	rc := &RootKubeConfig{}
	if authInfo, ok := config.AuthInfos[config.Contexts[config.CurrentContext].AuthInfo]; ok {
		if authInfo.Exec != nil {
			rc.Exec = authInfo.Exec.DeepCopy()
			authInfo.Exec = nil
		} else if authInfo.TokenFile != "" {
			baseDir := ""
			if authInfo.LocationOfOrigin != "" {
				baseDir = filepath.Dir(authInfo.LocationOfOrigin)
			}
			if rc.TokenFile, err = clientcmdapi.MakeAbs(authInfo.TokenFile, baseDir); err != nil {
				return nil, err
			}
		}
		authInfo.TokenFile = ""
		if rc.TokenFile != "" || rc.Exec != nil {
			authInfo.Token = ""
		}
	}
	if err = clientcmdapi.FlattenConfig(&config); err != nil {
		return nil, err
	}
	if rc.Data, err = clientcmd.Write(config); err != nil {
		return nil, err
	}
	return rc, nil
}

// anonymousUser is the name given to the user of a context that has none, when overrides gave that user credentials.
const anonymousUser = "anonymous"

// nameAnonymousUser gives the credentials that overrides set for a context without a user the name of
// a user of that context, because a minified config doesn't keep the unnamed user.
func nameAnonymousUser(config *clientcmdapi.Config, kubeContext string) {
	if kubeContext == "" {
		kubeContext = config.CurrentContext
	}
	if kc, ok := config.Contexts[kubeContext]; ok && kc.AuthInfo == "" {
		if ai, ok := config.AuthInfos[""]; ok {
			delete(config.AuthInfos, "")
			config.AuthInfos[anonymousUser] = ai
			kc.AuthInfo = anonymousUser
		}
	}
}

// minifiedConfig returns a copy of the given config that just contains the current (or given) context.
func minifiedConfig(ctx context.Context, origConfig clientcmdapi.Config, kubeContext string) (clientcmdapi.Config, error) {
	var config clientcmdapi.Config
	origConfig.DeepCopyInto(&config)

	// Minify the config so that we only deal with the current context.
	if kubeContext != "" {
		config.CurrentContext = kubeContext
	}
	if err := clientcmdapi.MinifyConfig(&config); err != nil {
		return config, err
	}
	clog.Debugf(ctx, "context = %q", config.CurrentContext)

	// Minify guarantees that the CurrentContext is set, but not that it has a cluster
	cc := config.Contexts[config.CurrentContext]
	if cc.Cluster == "" {
		return config, fmt.Errorf("current context %q has no cluster", config.CurrentContext)
	}
	return config, nil
}
