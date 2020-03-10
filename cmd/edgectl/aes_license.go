package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/datawire/ambassador/pkg/k8s"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

func aesLicense(cmd *cobra.Command, args []string) error {
	context, _ := cmd.Flags().GetString("context")
	namespace, _ := cmd.Flags().GetString("namespace")
	licenseKey := args[0]
	data := base64.StdEncoding.EncodeToString([]byte(licenseKey))
	manifest := fmt.Sprintf(secretManifest, namespace, data)

	kubeinfo := k8s.NewKubeInfo("", context, namespace)
	kargs, err := kubeinfo.GetKubectlArray("apply", "-f", "-")
	if err != nil {
		return errors.Wrap(err, "cluster access")
	}
	apply := exec.Command("kubectl", kargs...)
	apply.Stdin = strings.NewReader(manifest)
	apply.Stdout = os.Stdout
	apply.Stderr = os.Stderr
	err = apply.Run()
	if err != nil {
		return errors.Wrap(err, "kubectl apply")
	}

	fmt.Println("License applied!")
	return nil
}

const secretManifest = `
apiVersion: v1
kind: Secret
metadata:
  name: ambassador-edge-stack
  namespace: %s
data:
  license-key: "%s"
`
