package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	_ = res.Flags().BoolP(
		"verbose", "v", false,
		"Show all output. Defaults to sending most output to the logfile.",
	)
	return res
}

func getEmailAddress(defaultEmail string, log *log.Logger) string {
	prompt := fmt.Sprintf("Email address [%s]: ", defaultEmail)
	errorFallback := defaultEmail
	if defaultEmail == "" {
		prompt = "Email address: "
		errorFallback = "email_query_failure@datawire.io"
	}

	for {
		fmt.Print(prompt)
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Scan()
		text := scanner.Text()
		if err := scanner.Err(); err != nil {
			log.Printf("Email query failed: %+v", err)
			return errorFallback
		}

		text = strings.TrimSpace(text)
		if defaultEmail != "" && text == "" {
			return defaultEmail
		}

		if validEmailAddress.MatchString(text) {
			return text
		}

		fmt.Printf("Sorry, %q does not match our email address filter.\n", text)
	}
}

func aesInstall(cmd *cobra.Command, args []string) error {
	verbose, _ := cmd.Flags().GetBool("verbose")
	i := NewInstaller(verbose)
	i.Report("install")

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
	i.show.Printf("-> Installing the Ambassador Edge Stack %s\n", Version)

	// Attempt to talk to the specified cluster
	context, _ := cmd.Flags().GetString("context")
	i.kubeinfo = k8s.NewKubeInfo("", context, "")

	if err := i.ShowKubectl("cluster-info", "cluster-info"); err != nil {
		return err
	}

	if clusterNodeLabels, err := i.CaptureKubectl("get node labels", "get", "no", "-Lkubernetes.io/hostname"); err == nil {
		clusterInfo := "unknown"
		if clusterNodeLabels.Contains("docker-desktop") {
			clusterInfo = "docker-desktop"
		} else if clusterNodeLabels.Contains("minikube") {
			clusterInfo = "minikube"
		} else if clusterNodeLabels.Contains("gke") {
			clusterInfo = "gke"
		} else if clusterNodeLabels.Contains("aks") {
			clusterInfo = "aks"
		} else if clusterNodeLabels.Contains("compute") {
			clusterInfo = "eks"
		} else if clusterNodeLabels.Contains("ec2") {
			clusterInfo = "ec2"
		}
		i.SetMetadatum("Cluster Info", "cluster_info", clusterInfo)
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
			i.SetMetadatum("Cluster ID", "aes_install_id", clusterID)
			break
		}
		time.Sleep(500 * time.Millisecond) // FIXME: Time out at some point...
	}

	if managedDeployment, err := i.CaptureKubectl("get deployment labels", "get", "-n", "ambassador", "deployments", "ambassador", "-Lapp.kubernetes.io/managed-by"); err == nil {
		if managedDeployment.Contains("Helm") {
			i.SetMetadatum("Cluster Info", "managed", "helm")
		}
	}

	i.Report("deploy")

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

	i.show.Println("Your IP address is", ipAddress)

	// Wait for Ambassador to be ready to serve ACME requests.
	// FIXME: This assumes we can connect to the load balancer. If this
	// assumption is incorrect, this code will loop forever.
	for {
		// FIXME: Time out at some point...
		time.Sleep(500 * time.Millisecond)

		// Verify that we can connect to something
		resp, err := http.Get("http://" + ipAddress + "/.well-known/acme-challenge/")
		if err != nil {
			i.show.Printf("Waiting for Ambassador (get): %#v\n", err)
			continue
		}
		_, _ = ioutil.ReadAll(resp.Body)
		resp.Body.Close()

		// Verify that we get the expected status code. If Ambassador is still
		// starting up, then Envoy may return "upstream request timeout" (503),
		// in which case we should keep looping.
		if resp.StatusCode != 404 {
			i.show.Printf("Waiting for Ambassador: wrong status code: %d\n", resp.StatusCode)
			continue
		}

		// Sanity check that we're talking to Envoy. This is probably unnecessary.
		if resp.Header.Get("server") != "envoy" {
			i.show.Printf("Waiting for Ambassador: wrong server header: %s\n", resp.Header.Get("server"))
			continue
		}
		break
	}

	i.show.Println("-> Automatically configuring TLS")

	// Attempt to grab a reasonable default for the user's email address
	defaultEmail, err := i.Capture("get email", "git", "config", "--global", "user.email")
	if err != nil {
		i.log.Print(err)
		defaultEmail = ""
	} else {
		defaultEmail = strings.TrimSpace(defaultEmail)
		if !validEmailAddress.MatchString(defaultEmail) {
			defaultEmail = ""
		}
	}

	// Ask for the user's email address
	i.show.Println(emailAsk)
	emailAddress := getEmailAddress(defaultEmail, i.log)
	i.log.Printf("Using email address %q", emailAddress)

	// Send a request to acquire a DNS name for this cluster's IP address
	regURL := "https://metriton.datawire.io/beta/register-domain"
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
		i.show.Println("-> Acquiring DNS name", hostname)

		// Wait for DNS to propagate. This tries to avoid waiting for a ten
		// minute error backoff if the ACME registration races ahead of the DNS
		// name appearing for LetsEncrypt.
		for n := 0; n < 1200; n++ {
			conn, err := net.Dial("tcp", fmt.Sprintf("check-%d.%s:443", n, hostname))
			if err == nil {
				conn.Close()
				break
			}
			// i.show.Printf("Waiting for DNS: %#v\n", err)
			time.Sleep(500 * time.Millisecond)
		}
		// Create a Host resource
		hostResource := fmt.Sprintf(hostManifest, hostname, hostname, emailAddress)
		kargs, err := i.kubeinfo.GetKubectlArray("apply", "-f", "-")
		if err != nil {
			return errors.Wrapf(err, "cluster access for install AES")
		}
		i.log.Println("$ kubectl apply -f - < [Host Resource]")
		cmd := exec.Command("kubectl", kargs...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = strings.NewReader(hostResource)
		if err := cmd.Run(); err != nil {
			return errors.Wrap(err, "install AES")
		}

		i.show.Println("-> Obtaining a TLS certificate from Let's Encrypt")

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

		i.Report("cert_provisioned")
		i.show.Println("-> TLS configured successfully")
		if err := i.ShowKubectl("show Host", "get", "host", hostname); err != nil {
			return err
		}

		// Open a browser window to the Edge Policy Console
		if err := do_login(i.kubeinfo, context, "ambassador", hostname, false, false); err != nil {
			return err
		}

	} else {
		message := strings.TrimSpace(string(content))
		i.show.Println("-> Failed to create a DNS name:", message)
		i.show.Println()
		i.show.Println("If this IP address is reachable from here, then the following command")
		i.show.Println("will open the Edge Policy Console once you accept a self-signed")
		i.show.Println("certificate in your browser.")
		i.show.Println()
		i.show.Println("    edgectl login -n ambassador", ipAddress)
		i.show.Println()
		i.show.Println("If the IP is not reachable from here, you can use port forwarding to")
		i.show.Println("access the Edge Policy Console.")
		i.show.Println()
		i.show.Println("    kubectl -n ambassador port-forward deploy/ambassador 8443 &")
		i.show.Println("    edgectl login -n ambassador 127.0.0.1:8443")
		i.show.Println()
		i.show.Println("You will need to accept a self-signed certificate in your browser.")
		i.show.Println()
		i.show.Println("See https://www.getambassador.io/user-guide/getting-started/")
	}

	i.Report("aes_health_good") // or aes_health_bad TODO: Send cluster's install_id and AES version

	return nil
}

type Installer struct {
	kubeinfo *k8s.KubeInfo
	scout    *Scout
	show     *log.Logger
	log      *log.Logger
	cmdOut   *log.Logger
	cmdErr   *log.Logger
	logName  string
}

func NewInstaller(verbose bool) *Installer {
	// Although log, cmdOut, and cmdErr *can* go to different files and/or have
	// different prefixes, they'll probably all go to the same file, possibly
	// with different prefixes, for most cases.
	logfileName := filepath.Join(os.TempDir(), time.Now().Format("edgectl-install-20060102-150405.log"))
	logfile, err := os.Create(logfileName)
	if err != nil {
		logfile = os.Stderr
		fmt.Fprintf(logfile, "WARNING: Failed to open logfile %q: %+v\n", logfileName, err)
	}
	i := &Installer{
		scout:   NewScout("install"),
		show:    log.New(io.MultiWriter(os.Stdout, logfile), "", 0),
		logName: logfileName,
	}
	if verbose {
		i.log = log.New(io.MultiWriter(logfile, NewLoggingWriter(log.New(os.Stderr, "== ", 0))), "", log.Ltime)
		i.cmdOut = log.New(io.MultiWriter(logfile, NewLoggingWriter(log.New(os.Stderr, "=- ", 0))), "", 0)
		i.cmdErr = log.New(io.MultiWriter(logfile, NewLoggingWriter(log.New(os.Stderr, "=x ", 0))), "", 0)
	} else {
		i.log = log.New(logfile, "", log.Ltime)
		i.cmdOut = log.New(logfile, "", 0)
		i.cmdErr = log.New(logfile, "", 0)
	}

	return i
}

// Kubernetes Cluster

func (i *Installer) ShowKubectl(name string, args ...string) error {
	kargs, err := i.kubeinfo.GetKubectlArray(args...)
	if err != nil {
		return errors.Wrapf(err, "cluster access for %s", name)
	}
	i.log.Printf("$ kubectl %s", strings.Join(kargs, " "))
	cmd := exec.Command("kubectl", kargs...)
	cmd.Stdout = NewLoggingWriter(i.cmdOut)
	cmd.Stderr = NewLoggingWriter(i.cmdErr)
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
	kargs = append([]string{"kubectl"}, kargs...)
	return i.Capture(name, kargs...)
}

func (i *Installer) Capture(name string, args ...string) (res string, err error) {
	res = ""
	resAsBytes := &bytes.Buffer{}
	i.log.Printf("$ %s", strings.Join(args, " "))
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = io.MultiWriter(NewLoggingWriter(i.cmdOut), resAsBytes)
	cmd.Stderr = NewLoggingWriter(i.cmdErr)
	err = cmd.Run()
	if err != nil {
		err = errors.Wrap(err, name)
	}
	res = resAsBytes.String()
	return
}

// Metrics

func (i *Installer) SetMetadatum(name, key string, value interface{}) {
	i.log.Printf("[Metrics] %s (%q) is %q", name, key, value)
	i.scout.SetMetadatum(key, value)
}

func (i *Installer) Report(eventName string, meta ...ScoutMeta) {
	i.log.Println("[Metrics]", eventName)
	if err := i.scout.Report(eventName, meta...); err != nil {
		i.log.Println("[Metrics]", eventName, err)
	}
}

// DNS Registration

type registration struct {
	Email string
	Ip    string
}

const hostManifest = `
apiVersion: getambassador.io/v2
kind: Host
metadata:
  name: %s
spec:
  hostname: %s
  acmeProvider:
    email: %s
`

// FIXME: Mention that this will be shared with Let's Encrypt?
const emailAsk = `Please enter an email address. We'll use this email address to notify you
prior to domain and certificate expiration.`

var validEmailAddress = regexp.MustCompile("^[a-zA-Z0-9.!#$%&'*+/=?^_`{|}~-]+@[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(?:\\.[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$")
