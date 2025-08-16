package authenticator

import (
	"context"
	"fmt"

	clientcmd_api "k8s.io/client-go/tools/clientcmd/api"

	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

func NewService(
	clientConfigProvider k8sapi.ClientConfigProvider,
) *Service {
	return &Service{
		clientConfigProvider:    clientConfigProvider,
		execCredentialsResolver: execCredentialBinary{},
	}
}

type ExecCredentialsResolver interface {
	Resolve(
		ctx context.Context,
		execConfig *clientcmd_api.ExecConfig,
	) ([]byte, error)
}

type Service struct {
	clientConfigProvider    k8sapi.ClientConfigProvider
	execCredentialsResolver ExecCredentialsResolver
}

func (a Service) GetExecCredentials(ctx context.Context, contextName string) ([]byte, error) {
	execConfig, err := a.getExecConfigFromContext(contextName)
	if err != nil {
		return nil, fmt.Errorf("failed to get exec config from context %s, %w", contextName, err)
	}

	rawExecCredentials, err := a.execCredentialsResolver.Resolve(ctx, execConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve credentials: %w", err)
	}

	return rawExecCredentials, nil
}

func (a Service) getExecConfigFromContext(contextName string) (*clientcmd_api.ExecConfig, error) {
	cc, err := a.clientConfigProvider.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get kubeconfig provider: %w", err)
	}
	rawConfig, err := cc.RawConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get kubeconfig: %w", err)
	}

	kubeContext, ok := rawConfig.Contexts[contextName]
	if !ok {
		return nil, fmt.Errorf("kube context %s doesn't exist", contextName)
	}

	authInfo, ok := rawConfig.AuthInfos[kubeContext.AuthInfo]
	if !ok {
		return nil, fmt.Errorf("auth info %s doesn't exist", kubeContext.AuthInfo)
	}

	if authInfo.Exec == nil {
		return nil, fmt.Errorf("auth info %s isn't of type exec", kubeContext.AuthInfo)
	}

	return &clientcmd_api.ExecConfig{
		Command: authInfo.Exec.Command,
		Args:    authInfo.Exec.Args,
		Env:     authInfo.Exec.Env,
	}, nil
}
