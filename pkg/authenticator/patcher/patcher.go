package patcher

import (
	"fmt"
	"os"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const KubeConfigStubBinaryName = "authenticator"

// GenerateTempKubeConfigStubFile go through the kubeconfig file and replace all users using the Exec auth method by
// an invocation of the stub binary.
// It returns the temp kubeconfig file to mount, and the client config
func GenerateTempKubeConfigStubFile(originalKubeConfig clientcmd.ClientConfig) (string, error) {
	rawConfig, err := originalKubeConfig.RawConfig()
	if err != nil {
		return "", err
	}

	for contextName, kubeContext := range rawConfig.Contexts {
		// Find related Auth.
		authInfo, ok := rawConfig.AuthInfos[kubeContext.AuthInfo]
		if !ok {
			return "", fmt.Errorf("auth info %s not found for context %s", kubeContext.AuthInfo, contextName)
		}

		// If it isn't an exec mode context, just return the default host kubeconfig.
		if authInfo.Exec == nil {
			continue
		}

		// Patch exec.
		authInfo.Exec = &clientcmdapi.ExecConfig{
			APIVersion: authInfo.Exec.APIVersion,
			Command:    KubeConfigStubBinaryName,
			Args:       []string{contextName},
		}
		authInfo.Exec.Args = []string{contextName}
	}

	tmpConfFile, err := os.CreateTemp(os.TempDir(), "kubeconfig")
	if err != nil {
		return "", err
	}
	_ = tmpConfFile.Close()

	err = clientcmd.WriteToFile(rawConfig, tmpConfFile.Name())
	if err != nil {
		return "", err
	}

	return tmpConfFile.Name(), nil
}
