package patcher

import (
	"fmt"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const KubeConfigStubBinaryName = "authenticator"

// ReplaceAuthExecWithStub goes through the kubeconfig and replaces all uses of the Exec auth method by
// an invocation of the stub binary.
func ReplaceAuthExecWithStub(rawConfig *clientcmdapi.Config, address string) error {
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
			APIVersion: authInfo.Exec.APIVersion,
			Command:    KubeConfigStubBinaryName,
			Args:       []string{contextName, address},
		}
	}
	return nil
}

// NeedsStubbedExec returns true if the config contains at least one user with an Exec type AuthInfo.
func NeedsStubbedExec(rawConfig *clientcmdapi.Config) bool {
	for _, kubeContext := range rawConfig.Contexts {
		if authInfo, ok := rawConfig.AuthInfos[kubeContext.AuthInfo]; ok && authInfo.Exec != nil {
			return true
		}
	}
	return false
}
