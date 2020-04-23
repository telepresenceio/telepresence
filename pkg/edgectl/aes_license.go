package edgectl

import (
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	"github.com/datawire/ambassador/pkg/k8s"
)

func AESLicense(cmd *cobra.Command, args []string) error {
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
	output, err := apply.CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "kubectl apply: %s", output)
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
