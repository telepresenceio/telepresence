package patcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
)

const (
	kubeConfigStubSubCommands = "kubeauth"
	kubeConfigs               = "kube"
)

// AddressProvider is a function that returns the path to the telepresence executable and an address to a service that
// implements the Authenticator gRPC.
//
// The function will typically start the gRPC service, and the service is therefore given
// a list of files that it must listen to in order to reliably resolve requests. It is
// also passed a pointer to the minified config that will be stored in a file so that it
// has a chance to modify it.
type (
	AddressProvider func(configFiles []string) (string, string, error)
	Patcher         func(*clientcmdapi.Config) error
)

// CreateExternalKubeConfig will load the current kubeconfig and minimize it so that it just contains the current
// context. It will then check if that context contains an Exec config, and if it does, replace that config with
// an Exec config that instead runs a process that will use a gRPC call to the address returned by the given
// authAddressFunc.
func CreateExternalKubeConfig(ctx context.Context, kubeFlags map[string]string, authAddressFunc AddressProvider, patcher Patcher) (*clientcmdapi.Config, error) {
	configFlags, err := client.ConfigFlags(kubeFlags)
	if err != nil {
		return nil, err
	}

	loader := configFlags.ToRawKubeConfigLoader()
	ns, _, err := loader.Namespace()
	if err != nil {
		return nil, err
	}

	configFiles := loader.ConfigAccess().GetLoadingPrecedence()
	dlog.Debugf(ctx, "host kubeconfig = %v", configFiles)
	config, err := loader.RawConfig()
	if err != nil {
		return nil, err
	}

	// Minify the config so that we only deal with the current context.
	if cx := configFlags.Context; cx != nil && *cx != "" {
		config.CurrentContext = *cx
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
		executable, addr, err := authAddressFunc(configFiles)
		if err != nil {
			return nil, err
		}
		if err = replaceAuthExecWithStub(&config, executable, addr); err != nil {
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

	// Store the file using its context name under the <telepresence cache>/kube directory
	kubeConfigFile := ioutil.SafeName(config.CurrentContext)
	kubeConfigDir := filepath.Join(filelocation.AppUserCacheDir(ctx), kubeConfigs)
	if err = os.MkdirAll(kubeConfigDir, 0o700); err != nil {
		return nil, err
	}
	if err = clientcmd.WriteToFile(config, filepath.Join(kubeConfigDir, kubeConfigFile)); err != nil {
		return nil, err
	}
	return &config, nil
}

// replaceAuthExecWithStub goes through the kubeconfig and replaces all uses of the Exec auth method by
// an invocation of the stub binary.
func replaceAuthExecWithStub(rawConfig *clientcmdapi.Config, executable, address string) error {
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
			Args:            []string{kubeConfigStubSubCommands, contextName, address},
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

// AnnotateConnectRequest is used when the CLI connects to a containerized user-daemon. It adds a ContainerKubeFlagOverrides
// to the given ConnectRequest containing the path to the modified kubeconfig file to be used in the container.
func AnnotateConnectRequest(cr *connector.ConnectRequest, cacheDir, kubeContext string) {
	kubeConfigFile := ioutil.SafeName(kubeContext)
	if cr.ContainerKubeFlagOverrides == nil {
		cr.ContainerKubeFlagOverrides = make(map[string]string)
	}
	// Concatenate using "/". This will be used in linux
	cr.ContainerKubeFlagOverrides["kubeconfig"] = fmt.Sprintf("%s/%s/%s", cacheDir, kubeConfigs, kubeConfigFile)

	// We never instruct the remote containerized daemon to modify its KUBECONFIG environment.
	delete(cr.Environment, "KUBECONFIG")
	delete(cr.Environment, "-KUBECONFIG")
}

// AnnotateOutboundInfo is used when a non-containerized user-daemon connects to the root-daemon. The KubeFlags
// are modified to contain the path to the modified kubeconfig file.
func AnnotateOutboundInfo(ctx context.Context, oi *daemon.OutboundInfo, kubeContext string) {
	kubeConfigFile := ioutil.SafeName(kubeContext)
	if oi.KubeFlags == nil {
		oi.KubeFlags = make(map[string]string)
	} else {
		oi.KubeFlags = maps.Copy(oi.KubeFlags)
	}
	// Concatenate using "/". This will be used in linux
	oi.KubeFlags["kubeconfig"] = fmt.Sprintf("%s/%s/%s", filelocation.AppUserCacheDir(ctx), kubeConfigs, kubeConfigFile)
}
