package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/datawire/ambassador/pkg/k8s"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

func aesInstallCmd() *cobra.Command {
	res := &cobra.Command{
		Use:   "install",
		Short: "Install the Ambassador Edge Stack in your cluster",
		Args:  cobra.ExactArgs(0),
		RunE:  aesInstall,
	}
	_ = res.Flags().StringP(
		"context", "c", "",
		"The Kubernetes context to use. Defaults to the current kubectl context.",
	)
	_ = res.Flags().StringP(
		"namespace", "n", "",
		"The Kubernetes namespace to use. Defaults to kubectl's default for the context.",
	)

	return res
}

func aesInstall(cmd *cobra.Command, args []string) error {
	metrics := NewMetrics()
	_ = metrics.Report("install")

	// Display version information
	fmt.Printf("-> Installing the Ambassador Edge Stack %s\n", Version)

	// Attempt to talk to the specified cluster
	context, _ := cmd.Flags().GetString("context")
	namespace, _ := cmd.Flags().GetString("namespace")
	kubeinfo := k8s.NewKubeInfo("", context, namespace)
	i := &Installer{
		kubeinfo,
	}
	if err := i.ShowKubectl("cluster-info", "cluster-info"); err != nil {
		return err
	}

	if err := i.ShowKubectl("install CRDs", "apply", "-f", "https://www.getambassador.io/yaml/aes-crds.yaml"); err != nil {
		return err
	}

	if err := i.ShowKubectl("wait for CRDs", "wait", "--for", "condition=established", "--timeout=90s", "crd", "-lproduct=aes"); err != nil {
		return err
	}

	if err := i.ShowKubectl("install AES", "apply", "-f", "https://www.getambassador.io/yaml/aes.yaml"); err != nil {
		return err
	}

	if err := i.ShowKubectl("wait for AES", "-n", "ambassador", "wait", "--for", "condition=available", "--timeout=90s", "deploy", "-lproduct=aes"); err != nil {
		return err
	}

	_ = metrics.Report("deploy") // TODO: Send cluster type and Helm version

	ipAddress := ""
	for {
		var err error
		ipAddress, err = i.CaptureKubectl("get IP address", "get", "-n", "ambassador", "service", "ambassador", "-o", `go-template={{range .status.loadBalancer.ingress}}{{print .ip "\n"}}{{end}}`)
		if err != nil {
			return err
		}
		ipAddress = strings.TrimSpace(ipAddress)
		if ipAddress != "" {
			break
		}
		time.Sleep(250 * time.Millisecond) // FIXME: Time out at some point...
	}

	fmt.Println("Your IP address is", ipAddress)

	// Send a request to register this endpoint
	// if successful
	//   _ = metrics.Report("cert_provisioned")
	//   create a Host object with the info received
	//   report to the user
	//   open a browser window to https...
	// else
	//   report to the user
	//   if reachable from host (e.g., k3s, special case for Minikube?)
	//     open a browser window to http...
	//   else
	//     suggest port-forward to reach policy console?

	_ = metrics.Report("aes_health_good") // or aes_health_bad TODO: Send cluster's install_id and AES version

	return nil
}

type Installer struct {
	kubeinfo *k8s.KubeInfo
}

// Kubernetes Cluster

func (i *Installer) ShowKubectl(name string, args ...string) error {
	kargs, err := i.kubeinfo.GetKubectlArray(args...)
	if err != nil {
		return errors.Wrapf(err, "cluster access for %s", name)
	}
	fmt.Printf("\n$ kubectl %s\n", strings.Join(kargs, " "))
	cmd := exec.Command("kubectl", kargs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return errors.Wrap(err, name)
	}
	return nil
}

func (i *Installer) CaptureKubectl(name string, args ...string) (res string, err error) {
	res = ""
	kargs, err := i.kubeinfo.GetKubectlArray(args...)
	if err != nil {
		err = errors.Wrapf(err, "cluster access for %s", name)
		return
	}
	fmt.Printf("\n$ kubectl %s\n", strings.Join(kargs, " "))
	cmd := exec.Command("kubectl", kargs...)
	cmd.Stderr = nil
	resAsBytes, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			fmt.Println(ee.Stderr)
		}
		err = errors.Wrap(err, name)
	}
	res = string(resAsBytes)
	return
}

// Metrics

type Metrics struct {
	InstallID string
}

func NewMetrics() *Metrics {
	// TODO: Read or create an installation ID
	return nil
}

func (m *Metrics) Report(eventName string) error {
	fmt.Println("-> [Metrics]", eventName)
	return nil
}
