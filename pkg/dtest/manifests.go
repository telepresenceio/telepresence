package dtest

import (
	"fmt"
	"os"
	"time"

	"github.com/datawire/teleproxy/pkg/k8s"
	"github.com/datawire/teleproxy/pkg/kubeapply"
)

func Manifests(kubeconfig string, files ...string) {
	err := kubeapply.Kubeapply(k8s.NewKubeInfo(kubeconfig, "", ""), 30*time.Second, false, files...)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
