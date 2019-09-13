package dtest

import (
	"fmt"
	"os"
	"time"

	"github.com/datawire/teleproxy/pkg/k8s"
	"github.com/datawire/teleproxy/pkg/kubeapply"
	"github.com/datawire/teleproxy/pkg/supervisor"
)

// K8sApply applies the supplied manifests to the cluster indicated by
// the supplied kubeconfig.
func K8sApply(files ...string) {
	if os.Getenv("DOCKER_REGISTRY") == "" {
		os.Setenv("DOCKER_REGISTRY", DockerRegistry())
	}
	kubeconfig := Kubeconfig()
	err := kubeapply.Kubeapply(k8s.NewKubeInfo(kubeconfig, "", ""), 300*time.Second, false, false, files...)
	if err != nil {
		fmt.Println()
		fmt.Println(err)
		fmt.Printf(`
  Please note, if this is a timeout, then your kubernetes cluster may not
  exist or may be unreachable. Check access to your cluster with "kubectl --kubeconfig %s".

`, kubeconfig)
		fmt.Println()
		cmd := supervisor.Command(
			prefix, "kubectl", "--kubeconfig", kubeconfig,
			"get", "--all-namespaces", "ns,svc,deploy,po",
		)
		_ = cmd.Run() // Command output and any error will be logged

		os.Exit(1)
	}
}
