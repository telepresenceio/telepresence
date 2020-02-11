package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
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
	// TODO: This displays the version of Edge Control, not the image version in
	// the manifests being downloaded. We should figure out what to do if those
	// don't match up.
	// 1. Allow an old Edge Control to install a newer AES
	// 2. Insist that Edge Control be the same version as the AES it's installing
	// 3. Something else?
	// Note that the second option will allow us to make the install process
	// more complicated in the future without having to subject our users to
	// increasing complexity, assuming this approach to installation gains
	// traction.
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

	// Grab Ambassador's install ID as the cluster ID we'll send going forward.
	// Note: Using "kubectl exec" has the side effect of making sure the Pod is
	// Running (though not necessarily Ready). This should be good enough to
	// report the "deploy" status to metrics.
	for {
		// FIXME This doesn't work with `kubectl` 1.13 (and possibly 1.14). We
		// FIXME need to discover and use the pod name with `kubectl exec`.
		if clusterID, err := i.CaptureKubectl("get cluster ID", "-n", "ambassador", "exec", "deploy/ambassador", "python3", "kubewatch.py"); err == nil {
			metrics.SetClusterID(strings.TrimSpace(clusterID))
			break
		}
		time.Sleep(500 * time.Millisecond) // FIXME: Time out at some point...
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

	fmt.Println("\nYour IP address is", ipAddress)

	// Wait for Ambassador to be ready to serve ACME requests.
	// FIXME: This assumes we can connect to the load balancer. If this
	// assumption is incorrect, this code will loop forever.
	for {
		// FIXME: Time out at some point...
		time.Sleep(500 * time.Millisecond)

		// Verify that we can connect to something
		resp, err := http.Get("http://" + ipAddress + "/.well-known/acme-challenge/")
		if err != nil {
			fmt.Printf("Waiting for Ambassador (get): %#v\n", err)
			continue
		}
		_, _ = ioutil.ReadAll(resp.Body)
		resp.Body.Close()

		// Verify that we get the expected status code. If Ambassador is still
		// starting up, then Envoy may return "upstream request timeout" (503),
		// in which case we should keep looping.
		if resp.StatusCode != 404 {
			fmt.Printf("Waiting for Ambassador: wrong status code: %d\n", resp.StatusCode)
			continue
		}

		// Sanity check that we're talking to Envoy. This is probably unnecessary.
		if resp.Header.Get("server") != "envoy" {
			fmt.Printf("Waiting for Ambassador: wrong server header: %s\n", resp.Header.Get("server"))
			continue
		}
		break
	}

	// Send a request to acquire a DNS name for this cluster's IP address
	regURL := "https://metriton.datawire.io/beta/register-domain"
	emailAddress := "ark3+eci@datawire.io"
	buf := new(bytes.Buffer)
	_ = json.NewEncoder(buf).Encode(registration{emailAddress, ipAddress})
	resp, err := http.Post(regURL, "application/json", buf)
	if err != nil {
		return errors.Wrap(err, "acquire DNS name (post)")
	}
	content, err := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return errors.Wrap(err, "acquire DNS name (read body)")
	}

	if resp.StatusCode == 200 {
		hostname := string(content)
		fmt.Println("-> Acquiring DNS name", hostname)

		// Wait for DNS to propagate. This tries to avoid waiting for a ten
		// minute error backoff if the ACME registration races ahead of the DNS
		// name appearing for LetsEncrypt.
		for {
			conn, err := net.Dial("tcp", hostname+":443")
			if err == nil {
				conn.Close()
				break
			}
			// fmt.Printf("Waiting for DNS: %#v\n", err)
			time.Sleep(500 * time.Millisecond)
		}
		fmt.Println("-> Automatically configuring TLS")
		fmt.Println("Please enter an email address. We'll use this email address to notify you prior to domain and certification expiration [None]:", emailAddress)
		fmt.Println("FIXME: let the user enter an address")
		// Create a Host resource
		hostResource := fmt.Sprintf(hostManifest, hostname, namespace, hostname, emailAddress)
		kargs, err := i.kubeinfo.GetKubectlArray("apply", "-f", "-")
		if err != nil {
			return errors.Wrapf(err, "cluster access for install AES")
		}
		fmt.Println("\n$ kubectl apply -f - < [Host Resource]")
		cmd := exec.Command("kubectl", kargs...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = strings.NewReader(hostResource)
		if err := cmd.Run(); err != nil {
			return errors.Wrap(err, "install AES")
		}

		fmt.Println("\n-> Obtaining a TLS certificate from Let's Encrypt")

		for {
			state, err := i.CaptureKubectl("get Host state", "get", "host", hostname, "-o", "go-template={{.status.state}}")
			if err != nil {
				return err
			}
			if state == "Ready" {
				break
			}
			time.Sleep(500 * time.Millisecond) // FIXME: Time out at some point...
			// FIXME: Do something smart for state == "Error"
		}

		_ = metrics.Report("cert_provisioned")
		fmt.Println("-> TLS configured successfully")
		if err := i.ShowKubectl("show Host", "get", "host", hostname); err != nil {
			return err
		}

		// Open a browser window to the Edge Policy Console
		if err := do_login(kubeinfo, context, "ambassador", hostname, false, false); err != nil {
			return err
		}

	} else {
		message := strings.TrimSpace(string(content))
		fmt.Println("\n-> Failed to create a DNS name:", message)
		fmt.Println()
		fmt.Println("If this IP address is reachable from here, then the following command")
		fmt.Println("will open the Edge Policy Console once you accept a self-signed")
		fmt.Println("certificate in your browser.")
		fmt.Println()
		fmt.Println("    edgectl login -n ambassador", ipAddress)
		fmt.Println()
		fmt.Println("If the IP is not reachable from here, you can use port forwarding to")
		fmt.Println("access the Edge Policy Console.")
		fmt.Println()
		fmt.Println("    kubectl -n ambassador port-forward deploy/ambassador 8443 &")
		fmt.Println("    edgectl login -n ambassador 127.0.0.1:8443")
		fmt.Println()
		fmt.Println("You will need to accept a self-signed certificate in your browser.")
		fmt.Println()
		fmt.Println("See https://www.getambassador.io/user-guide/getting-started/")
	}

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
			fmt.Println(string(ee.Stderr))
		}
		err = errors.Wrap(err, name)
	}
	res = string(resAsBytes)
	fmt.Println(res)
	return
}

// DNS Registration

type registration struct {
	Email string
	Ip    string
}

// Metrics

type Metrics struct {
	scout *Scout
}

func NewMetrics() *Metrics {
	scout, err := NewScout("install")
	if err != nil {
		// Don't crash if Scout stuff doesn't work
		scout = nil
	}
	return &Metrics{scout}
}

func (m *Metrics) SetClusterID(clusterID string) {
	fmt.Println("\n-> [Metrics] Cluster ID (AES install ID) is", clusterID)
	if m.scout != nil {
		m.scout.SetClusterID(clusterID)
	}
}

func (m *Metrics) Report(eventName string, meta ...ScoutMeta) error {
	fmt.Println("\n-> [Metrics]", eventName)
	if m.scout != nil {
		if err := m.scout.Report(eventName, meta...); err != nil {
			fmt.Println("            ", err)
		}
	}
	return nil
}

const hostManifest = `
apiVersion: getambassador.io/v2
kind: Host
metadata:
  name: %s
  namespace: %s
spec:
  hostname: %s
  acmeProvider:
    email: %s
`
