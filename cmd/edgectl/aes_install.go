package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/pkg/browser"

	"github.com/datawire/ambassador/pkg/k8s"
	"github.com/datawire/ambassador/pkg/supervisor"
	"github.com/gookit/color"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	k8sTypesMetaV1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sVersion "k8s.io/apimachinery/pkg/version"
	k8sClientCoreV1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
)

var validEmailAddress = regexp.MustCompile("^[a-zA-Z0-9.!#$%&'*+/=?^_`{|}~-]+@[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(?:\\.[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$")

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

		fmt.Printf("Sorry, %q does not appear to be a valid email address.  Please check it and try again.\n", text)
	}
}

func aesInstall(cmd *cobra.Command, args []string) error {
	skipReport, _ := cmd.Flags().GetBool("no-report")
	verbose, _ := cmd.Flags().GetBool("verbose")
	kcontext, _ := cmd.Flags().GetString("context")
	i := NewInstaller(verbose)

	// If Scout is disabled (environment variable set to non-null), inform the user.
	if i.scout.Disabled() {
		i.show.Printf("INFO: phone-home is disabled by environment variable")
	}

	// Both printed and logged when verbose (Installer.log is responsible for --verbose)
	i.log.Printf(fmt.Sprintf("INFO: install_id = %v; trace_id = %v", i.scout.installID, i.scout.metadata["trace_id"]))

	sup := supervisor.WithContext(i.ctx)
	sup.Logger = i.log

	sup.Supervise(&supervisor.Worker{
		Name: "signal",
		Work: func(p *supervisor.Process) error {
			sigs := make(chan os.Signal, 1)
			signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
			p.Ready()
			select {
			case sig := <-sigs:
				i.Report("user_interrupted", ScoutMeta{"signal", fmt.Sprintf("%+v", sig)})
				i.Quit()
			case <-p.Shutdown():
			}
			return nil
		},
	})
	sup.Supervise(&supervisor.Worker{
		Name:     "install",
		Requires: []string{"signal"},
		Work: func(p *supervisor.Process) error {
			defer i.Quit()
			result := i.Perform(kcontext)
			i.ShowResult(result)
			return result.Err
		},
	})

	// Don't allow messages emitted while opening the browser to mess up our
	// carefully-crafted terminal output
	browser.Stdout = ioutil.Discard
	browser.Stderr = ioutil.Discard

	runErrors := sup.Run()
	if len(runErrors) > 1 { // This shouldn't happen...
		for _, err := range runErrors {
			i.show.Printf(err.Error())
		}
	}
	if len(runErrors) > 0 {
		if !skipReport {
			i.generateCrashReport(runErrors[0])
		}
		i.show.Println()
		i.show.Printf("Full logs at %s\n\n", i.logName)
		return runErrors[0]
	}
	return nil
}

func getManifest(url string) (string, error) {
	res, err := http.Get(url)
	if err != nil {
		return "", err
	}
	bodyBytes, err := ioutil.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		return "", err
	}
	if res.StatusCode != 200 {
		return "", errors.Errorf("Bad status: %d", res.StatusCode)
	}
	return string(bodyBytes), nil
}

// LoopFailedError is a fatal error for loopUntil(...)
type LoopFailedError string

// Error implements error
func (s LoopFailedError) Error() string {
	return string(s)
}

type loopConfig struct {
	sleepTime    time.Duration // How long to sleep between calls
	progressTime time.Duration // How long until we explain why we're waiting
	timeout      time.Duration // How long until we give up
}

var lc2 = &loopConfig{
	sleepTime:    500 * time.Millisecond,
	progressTime: 15 * time.Second,
	timeout:      120 * time.Second,
}

var lc5 = &loopConfig{
	sleepTime:    3 * time.Second,
	progressTime: 30 * time.Second,
	timeout:      5 * time.Minute,
}

// loopUntil repeatedly calls a function until it succeeds, using a
// (presently-fixed) loop period and timeout.
func (i *Installer) loopUntil(what string, how func() error, lc *loopConfig) error {
	ctx, cancel := context.WithTimeout(i.ctx, lc.timeout)
	defer cancel()
	start := time.Now()
	i.log.Printf("Waiting for %s", what)
	defer func() { i.log.Printf("Wait for %s took %.1f seconds", what, time.Since(start).Seconds()) }()
	progTimer := time.NewTimer(lc.progressTime)
	defer progTimer.Stop()
	for {
		err := how()
		if err == nil {
			return nil // Success
		} else if _, ok := err.(LoopFailedError); ok {
			return err // Immediate failure
		}
		// Wait and try again
		select {
		case <-progTimer.C:
			i.ShowWaiting(what)
		case <-time.After(lc.sleepTime):
			// Try again
		case <-ctx.Done():
			i.ShowTimedOut(what)
			return errors.Errorf("timed out waiting for %s (or interrupted)", what)
		}
	}
}

// GrabAESInstallID uses "kubectl exec" to ask the AES pod for the cluster's ID,
// which we uses as the AES install ID. This has the side effect of making sure
// the Pod is Running (though not necessarily Ready). This should be good enough
// to report the "deploy" status to metrics.
func (i *Installer) GrabAESInstallID() error {
	aesImage := "quay.io/datawire/aes:" + i.version
	podName := ""
	containerName := ""
	podInterface := i.coreClient.Pods("ambassador") // namespace
	i.log.Print("> k -n ambassador get po")
	pods, err := podInterface.List(k8sTypesMetaV1.ListOptions{})
	if err != nil {
		return err
	}

	// Find an AES Pod
PodsLoop:
	for _, pod := range pods.Items {
		i.log.Print("  Pod: ", pod.Name)
	ContainersLoop:
		for _, container := range pod.Spec.Containers {
			// Avoid matching the Traffic Manager (container.Command == ["traffic-proxy"])
			i.log.Printf("       Container: %s (image: %q; command: %q)", container.Name, container.Image, container.Command)
			if container.Image != aesImage || container.Command != nil {
				continue
			}
			// Avoid matching the Traffic Agent by checking for
			// AGENT_SERVICE in the environment. This is how Ambassador's
			// Python code decides it is running as an Agent.
			for _, envVar := range container.Env {
				if envVar.Name == "AGENT_SERVICE" && envVar.Value != "" {
					i.log.Printf("                  AGENT_SERVICE: %q", envVar.Value)
					continue ContainersLoop
				}
			}
			i.log.Print("       Success")
			podName = pod.Name
			containerName = container.Name
			break PodsLoop
		}
	}
	if podName == "" {
		return errors.New("no AES pods found")
	}

	// Retrieve the cluster ID
	clusterID, err := i.CaptureKubectl("get cluster ID", "", "-n", "ambassador", "exec", podName, "-c", containerName, "python3", "kubewatch.py")
	if err != nil {
		return err
	}
	i.clusterID = clusterID
	i.SetMetadatum("Cluster ID", "aes_install_id", clusterID)
	return nil
}

// GrabLoadBalancerAddress retrieves the AES service load balancer's address (IP
// address or hostname)
func (i *Installer) GrabLoadBalancerAddress() error {
	serviceInterface := i.coreClient.Services("ambassador") // namespace
	service, err := serviceInterface.Get("ambassador", k8sTypesMetaV1.GetOptions{})
	if err != nil {
		return err
	}
	for _, ingress := range service.Status.LoadBalancer.Ingress {
		if net.ParseIP(ingress.IP) != nil {
			i.address = ingress.IP
			return nil
		}
		if ingress.Hostname != "" {
			i.address = ingress.Hostname
			return nil
		}
	}
	return errors.New("no address found")
}

// CheckAESServesACME performs the same checks that the edgestack.me name
// service performs against the AES load balancer host
func (i *Installer) CheckAESServesACME() (err error) {
	defer func() {
		if err != nil {
			i.log.Print(err.Error())
		}
	}()

	// Verify that we can connect to something
	resp, err := http.Get("http://" + i.address + "/.well-known/acme-challenge/")
	if err != nil {
		err = errors.Wrap(err, "check for AES")
		return
	}
	_, _ = ioutil.ReadAll(resp.Body)
	resp.Body.Close()

	// Verify that we get the expected status code. If Ambassador is still
	// starting up, then Envoy may return "upstream request timeout" (503),
	// in which case we should keep looping.
	if resp.StatusCode != 404 {
		err = errors.Errorf("check for AES: wrong status code: %d instead of 404", resp.StatusCode)
		return
	}

	// Sanity check that we're talking to Envoy. This is probably unnecessary.
	if resp.Header.Get("server") != "envoy" {
		err = errors.Errorf("check for AES: wrong server header: %s instead of envoy", resp.Header.Get("server"))
		return
	}

	return nil
}

// CheckAESHealth retrieves AES's idea of whether it is healthy, i.e. ready.
func (i *Installer) CheckAESHealth() error {
	resp, err := http.Get("https://" + i.hostname + "/ambassador/v0/check_ready")
	if err != nil {
		return err
	}
	_, _ = ioutil.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != 200 {
		return errors.Errorf("check for AES health: wrong status code: %d instead of 200", resp.StatusCode)
	}

	return nil
}

// CheckHostnameFound tries to connect to check-blah.hostname to see whether DNS
// has propagated. Each connect talks to a different hostname to try to avoid
// NXDOMAIN caching.
func (i *Installer) CheckHostnameFound() error {
	conn, err := net.Dial("tcp", fmt.Sprintf("check-%d.%s:443", time.Now().Unix(), i.hostname))
	if err == nil {
		conn.Close()
	}
	return err
}

// CheckACMEIsDone queries the Host object and succeeds if its state is Ready.
func (i *Installer) CheckACMEIsDone() error {
	state, err := i.CaptureKubectl("get Host state", "", "get", "host", i.hostname, "-o", "go-template={{.status.state}}")
	if err != nil {
		return LoopFailedError(err.Error())
	}
	if state == "Error" {
		reason, err := i.CaptureKubectl("get Host error", "", "get", "host", i.hostname, "-o", "go-template={{.status.errorReason}}")
		if err != nil {
			return LoopFailedError(err.Error())
		}
		// This heuristic tries to detect whether the error is that the ACME
		// provider got NXDOMAIN for the provided hostname. It specifically
		// handles the error message returned by Let's Encrypt in Feb 2020, but
		// it may cover others as well. The AES ACME controller retries much
		// sooner if this heuristic is tripped, so we should continue to wait
		// rather than giving up.
		isAcmeNxDomain := strings.Contains(reason, "NXDOMAIN") || strings.Contains(reason, "urn:ietf:params:acme:error:dns")
		if isAcmeNxDomain {
			return errors.New("Waiting for NXDOMAIN retry")
		}

		// TODO: Windows incompatible, will not be bold but otherwise functions.
		// TODO: rewrite Installer.show to make explicit calls to color.Bold.Printf(...) instead,
		// TODO: along with logging.  Search for color.Bold to find usages.

		i.show.Println()
		i.show.Println(color.Bold.Sprintf("Acquiring TLS certificate via ACME has failed: %s", reason))
		return LoopFailedError(fmt.Sprintf("ACME failed. More information: kubectl get host %s -o yaml", i.hostname))
	}
	if state != "Ready" {
		return errors.Errorf("Host state is %s, not Ready", state)
	}
	return nil
}

// GetAESCRDs returns the names of the AES CRDs available in the cluster (and
// logs them as a side effect)
func (i *Installer) GetAESCRDs() ([]string, error) {
	crds, err := i.CaptureKubectl("get AES crds", "", "get", "crds", "-lproduct=aes", "-o", "name")
	if err != nil {
		return nil, err
	}
	res := make([]string, 0, 15)
	scanner := bufio.NewScanner(strings.NewReader(crds))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			res = append(res, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return res, nil
}

// GetInstalledImageVersion returns the image of the installed AES deployment or the
// empty string if none is found.
// TODO: Try to search all namespaces (which may fail due to RBAC) and capture a
// correct namespace for an Ambassador installation (what if there is more than
// one?), then proceed operating on that Ambassador in that namespace. Right now
// we hard-code the "ambassador" namespace in a number of spots.
// TODO: Also look for Ambassador OSS and do something intelligent.
func (i *Installer) GetInstalledImageVersion() (string, error) {
	aesVersionRE := regexp.MustCompile("quay[.]io/datawire/aes:([[:^space:]]+)")
	deploys, err := i.CaptureKubectl("get AES deployment", "", "-nambassador", "get", "deploy", "-lproduct=aes", "-o", "go-template={{range .items}}{{range .spec.template.spec.containers}}{{.image}}\n{{end}}{{end}}")
	if err != nil {
		return "", err
	}
	scanner := bufio.NewScanner(strings.NewReader(deploys))
	for scanner.Scan() {
		image := strings.TrimSpace(scanner.Text())
		if matches := aesVersionRE.FindStringSubmatch(image); len(matches) == 2 {
			return matches[1], nil
		}
	}
	return "", scanner.Err()
}

// Perform is the main function for the installer
func (i *Installer) Perform(kcontext string) Result {
	// Start
	i.Report("install")

	i.show.Println()
	i.show.Println(color.Bold.Sprintf("Installing the Ambassador Edge Stack"))

	// Attempt to grab a reasonable default for the user's email address
	defaultEmail, err := i.Capture("get email", true, "", "git", "config", "--global", "user.email")
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
	i.show.Println()
	i.ShowWrapped("Please enter an email address for us to notify you before your TLS certificate and domain name expire. In order to acquire the TLS certificate, we share this email with Let’s Encrypt.")

	// Do the goroutine dance to let the user hit Ctrl-C at the email prompt
	gotEmail := make(chan string)
	var emailAddress string
	go func() {
		gotEmail <- getEmailAddress(defaultEmail, i.log)
		close(gotEmail)
	}()
	select {
	case emailAddress = <-gotEmail:
		// Continue
	case <-i.ctx.Done():
		return i.EmailRequestError(errors.New("Interrupted"))
	}
	i.show.Println()
	i.log.Printf("Using email address %q", emailAddress)

	// Beginning the AES Installation
	i.ShowBeginAESInstallation()

	// Attempt to use kubectl
	_, err = i.GetKubectlPath()
	if err != nil {
		return i.NoKubectlError(err)
	}

	// Attempt to talk to the specified cluster
	i.kubeinfo = k8s.NewKubeInfo("", kcontext, "")
	if err := i.ShowKubectl("cluster-info", "", "cluster-info"); err != nil {
		return i.NoClusterError(err)
	}
	i.restConfig, err = i.kubeinfo.GetRestConfig()
	if err != nil {
		return i.GetRestConfigError(err)
	}

	i.coreClient, err = k8sClientCoreV1.NewForConfig(i.restConfig)
	if err != nil {
		return i.NewForConfigError(err)
	}

	versions, err := i.CaptureKubectl("get versions", "", "version", "-o", "json")
	if err != nil {
		return i.GetVersionsError(err)
	}

	kubernetesVersion := &kubernetesVersion{}
	err = json.Unmarshal([]byte(versions), kubernetesVersion)
	if err != nil {
		// We tried to extract Kubernetes client and server versions but failed.
		// This should not happen since we already validated the cluster-info, but still...
		// It's not critical if this information is missing, other than for debugging purposes.
		i.log.Printf("failed to read Kubernetes client and server versions: %v", err.Error())
	}

	i.k8sVersion = *kubernetesVersion
	// Metriton tries to parse fields with `version` in their keys and discards them if it can't.
	// Using _v to keep the version value as string since Kubernetes versions vary in formats.
	i.SetMetadatum("kubectl Version", "kubectl_v", i.k8sVersion.Client.GitVersion)
	i.SetMetadatum("K8s Version", "k8s_v", i.k8sVersion.Server.GitVersion)

	// Allow overriding the source domain (e.g., for smoke tests before release)
	manifestsDomain := "www.getambassador.io"
	domainOverrideVar := "AES_MANIFESTS_DOMAIN"
	overrideMessage := "Downloading manifests from override domain %q instead of default %q because the environment variable %s is set."
	if amd := os.Getenv(domainOverrideVar); amd != "" {
		i.ShowWrapped(fmt.Sprintf(overrideMessage, amd, manifestsDomain, domainOverrideVar))
		manifestsDomain = amd
	}

	// Download AES manifests
	crdManifests, err := getManifest(fmt.Sprintf("https://%s/yaml/aes-crds.yaml", manifestsDomain))
	if err != nil {
		return i.AESCRDManifestsError(err)
	}

	aesManifests, err := getManifest(fmt.Sprintf("https://%s/yaml/aes.yaml", manifestsDomain))
	if err != nil {
		return i.AESManifestsError(err)
	}

	// Figure out what version of AES is being installed
	aesVersionRE := regexp.MustCompile("image: quay[.]io/datawire/aes:([[:^space:]]+)[[:space:]]")
	matches := aesVersionRE.FindStringSubmatch(aesManifests)
	if len(matches) != 2 {
		return i.ManifestParsingError(err, matches)
	}

	i.version = matches[1]
	i.SetMetadatum("AES version being installed", "aes_version", i.version)

	// Display version information
	i.ShowAESVersionBeingInstalled()

	// Try to determine cluster type from node labels
	isKnownLocalCluster := false
	if clusterNodeLabels, err := i.CaptureKubectl("get node labels", "", "get", "no", "-Lkubernetes.io/hostname"); err == nil {
		clusterInfo := "unknown"
		if strings.Contains(clusterNodeLabels, "docker-desktop") {
			clusterInfo = "docker-desktop"
			isKnownLocalCluster = true
		} else if strings.Contains(clusterNodeLabels, "minikube") {
			clusterInfo = "minikube"
			isKnownLocalCluster = true
		} else if strings.Contains(clusterNodeLabels, "kind") {
			clusterInfo = "kind"
			isKnownLocalCluster = true
		} else if strings.Contains(clusterNodeLabels, "gke") {
			clusterInfo = "gke"
		} else if strings.Contains(clusterNodeLabels, "aks") {
			clusterInfo = "aks"
		} else if strings.Contains(clusterNodeLabels, "compute") {
			clusterInfo = "eks"
		} else if strings.Contains(clusterNodeLabels, "ec2") {
			clusterInfo = "ec2"
		}
		i.SetMetadatum("Cluster Info", "cluster_info", clusterInfo)
	}

	// Find existing AES CRDs
	aesCrds, err := i.GetAESCRDs()
	if err != nil {
		i.show.Println("Failed to get existing CRDs:", err)
		aesCrds = []string{}
		// Things will likely fail when we try to apply CRDs
	}

	// Figure out whether we need to apply the manifests
	// TODO: Parse the downloaded manifests and look for specific CRDs.
	// Installed CRDs may be out of date or incomplete (e.g., if there's an
	// OSS installation present).
	alreadyApplied := false

	if len(aesCrds) > 0 {
		// AES CRDs exist so there is likely an existing installation. Try to
		// verify the existence of an Ambassador deployment in the Ambassador
		// namespace.
		installedVersion, err := i.GetInstalledImageVersion()
		if err != nil {
			i.show.Println("Failed to look for an existing installation:", err)
			installedVersion = ""
			// Things will likely fail when we try to apply manifests
		}

		switch {
		case i.version == installedVersion:
			alreadyApplied = true
			i.ShowAESExistingVersion(i.version)
		case installedVersion != "":
			i.ShowAESExistingVersion(installedVersion)
			return i.IncompatibleCRDVersionsError(installedVersion)
		default:
			i.ShowAESCRDsButNoAESInstallation()
			return i.ExistingCRDsError()
		}
	}

	if !alreadyApplied {
		// Install the AES manifests

		i.ShowDownloadingImages()

		if err := i.ShowKubectl("install CRDs", crdManifests, "apply", "-f", "-"); err != nil {
			return i.InstallCRDsError(err)
		}

		if err := i.ShowKubectl("wait for CRDs", "", "wait", "--for", "condition=established", "--timeout=90s", "crd", "-lproduct=aes"); err != nil {
			return i.WaitCRDsError(err)
		}

		if err := i.ShowKubectl("install AES", aesManifests, "apply", "-f", "-"); err != nil {
			return i.InstallAESError(err)
		}

		if err := i.ShowKubectl("wait for AES", "", "-n", "ambassador", "wait", "--for", "condition=available", "--timeout=90s", "deploy", "-lproduct=aes"); err != nil {
			return i.WaitForAESError(err)
		}
	}

	// Wait for Ambassador Pod; grab AES install ID
	i.ShowCheckingAESPodDeployment()
	if err := i.loopUntil("AES pod startup", i.GrabAESInstallID, lc2); err != nil {
		return i.AESPodStartupError(err)
	}

	// Grab Helm information if present
	if managedDeployment, err := i.CaptureKubectl("get deployment labels", "", "get", "-n", "ambassador", "deployments", "ambassador", "-Lapp.kubernetes.io/managed-by"); err == nil {
		if strings.Contains(managedDeployment, "Helm") {
			i.SetMetadatum("Cluster Info", "managed", "helm")
		}
	}

	i.Report("deploy")

	// Don't proceed any further if we know we are using a local (not publicly
	// accessible) cluster. There's no point wasting the user's time on
	// timeouts.

	if isKnownLocalCluster {
		i.ShowLocalClusterDetected()
		return i.KnownLocalClusterResult()
	}

	// Grab load balancer address
	i.ShowProvisioningLoadBalancer()

	if err := i.loopUntil("Load Balancer", i.GrabLoadBalancerAddress, lc5); err != nil {
		return i.LoadBalancerError(err)
	}
	i.Report("cluster_accessible")
	i.ShowAESInstallAddress(i.address)

	// Wait for Ambassador to be ready to serve ACME requests.
	i.ShowAESRespondingToACME()
	if err := i.loopUntil("AES to serve ACME", i.CheckAESServesACME, lc2); err != nil {
		return i.AESACMEChallengeError(err)
	}
	i.Report("aes_listening")
	i.ShowAESConfiguringTLS()

	// Send a request to acquire a DNS name for this cluster's load balancer
	regURL := "https://metriton.datawire.io/register-domain"
	regData := &registration{Email: emailAddress}
	if !i.scout.Disabled() {
		regData.AESInstallId = i.clusterID
		regData.EdgectlInstallId = i.scout.installID
	}
	if net.ParseIP(i.address) != nil {
		regData.Ip = i.address
	} else {
		regData.Hostname = i.address
	}
	buf := new(bytes.Buffer)
	_ = json.NewEncoder(buf).Encode(regData)
	resp, err := http.Post(regURL, "application/json", buf)
	if err != nil {
		return i.DNSNamePostError(err)
	}

	content, err := ioutil.ReadAll(resp.Body)
	resp.Body.Close()

	if err != nil {
		return i.DNSNameBodyError(err)
	}

	if resp.StatusCode != 200 {
		message := strings.TrimSpace(string(content))
		i.ShowFailedToCreateDNSName(message)
		return i.AESInstalledNoDNSResult(resp.StatusCode, message)
	}

	i.hostname = string(content)
	i.ShowAcquiringDNSName(i.hostname)

	// Wait for DNS to propagate. This tries to avoid waiting for a ten
	// minute error backoff if the ACME registration races ahead of the DNS
	// name appearing for LetsEncrypt.
	if err := i.loopUntil("DNS propagation to this host", i.CheckHostnameFound, lc2); err != nil {
		return i.DNSPropagationError(err)
	}

	i.Report("dns_name_propagated")

	// Create a Host resource
	hostResource := fmt.Sprintf(hostManifest, i.hostname, i.hostname, emailAddress)
	if err := i.ShowKubectl("install Host resource", hostResource, "apply", "-f", "-"); err != nil {
		return i.HostResourceCreationError(err)
	}

	i.ShowObtainingTLSCertificate()

	if err := i.loopUntil("TLS certificate acquisition", i.CheckACMEIsDone, lc5); err != nil {
		return i.CertificateProvisionError(err)
	}
	i.Report("cert_provisioned")
	i.ShowTLSConfiguredSuccessfully()
	if err := i.ShowKubectl("show Host", "", "get", "host", i.hostname); err != nil {
		return i.HostRetrievalError(err)
	}

	// All done!
	i.ShowAESInstallationComplete()

	// Open a browser window to the Edge Policy Console
	if err := do_login(i.kubeinfo, kcontext, "ambassador", i.hostname, true, true, false); err != nil {
		return i.AESLoginError(err)
	}

	// Show how to use edgectl login in the future
	i.show.Println()

	futureLogin := `In the future, to log in to the Ambassador Edge Policy Console, run %s`
	i.ShowWrapped(fmt.Sprintf(futureLogin, color.Bold.Sprintf("edgectl login "+i.hostname)))

	if err := i.CheckAESHealth(); err != nil {
		i.Report("aes_health_bad", ScoutMeta{"err", err.Error()})
	} else {
		i.Report("aes_health_good")
	}

	return i.AESLoginSuccessResult()
}

// Installer represents the state of the installation process
type Installer struct {
	// Cluster

	kubeinfo   *k8s.KubeInfo
	restConfig *rest.Config
	coreClient *k8sClientCoreV1.CoreV1Client

	// Reporting

	scout *Scout

	// Logging and management

	ctx     context.Context
	cancel  context.CancelFunc
	show    *log.Logger
	log     *log.Logger
	cmdOut  *log.Logger
	cmdErr  *log.Logger
	logName string

	// Install results

	k8sVersion kubernetesVersion // cluster version information
	version    string            // which AES is being installed
	address    string            // load balancer address
	hostname   string            // of the Host resource
	clusterID  string            // the Ambassador unique clusterID
}

// NewInstaller returns an Installer object after setting up logging.
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
	ctx, cancel := context.WithCancel(context.Background())
	i := &Installer{
		scout:   NewScout("install"),
		ctx:     ctx,
		cancel:  cancel,
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

func (i *Installer) Quit() {
	i.cancel()
}

func (i *Installer) ShowWrapped(text string) {
	text = strings.Trim(text, "\n")                  // Drop leading and trailing newlines
	for _, para := range strings.Split(text, "\n") { // Preserve newlines in the text
		for _, line := range doWordWrap(para, "", 79) { // But wrap text too
			i.show.Println(line)
		}
	}
}

// Kubernetes Cluster

// ShowKubectl calls kubectl and dumps the output to the logger. Use this for
// side effects.
func (i *Installer) ShowKubectl(name string, input string, args ...string) error {
	kargs, err := i.kubeinfo.GetKubectlArray(args...)
	if err != nil {
		return errors.Wrapf(err, "cluster access for %s", name)
	}
	kubectl, err := i.GetKubectlPath()
	if err != nil {
		return errors.Wrapf(err, "kubectl not found %s", name)
	}
	i.log.Printf("$ %v %s", kubectl, strings.Join(kargs, " "))
	cmd := exec.Command(kubectl, kargs...)
	cmd.Stdin = strings.NewReader(input)
	cmd.Stdout = NewLoggingWriter(i.cmdOut)
	cmd.Stderr = NewLoggingWriter(i.cmdErr)
	if err := cmd.Run(); err != nil {
		return errors.Wrap(err, name)
	}
	return nil
}

// CaptureKubectl calls kubectl and returns its stdout, dumping all the output
// to the logger.
func (i *Installer) CaptureKubectl(name, input string, args ...string) (res string, err error) {
	res = ""
	kargs, err := i.kubeinfo.GetKubectlArray(args...)
	if err != nil {
		err = errors.Wrapf(err, "cluster access for %s", name)
		return
	}
	kubectl, err := i.GetKubectlPath()
	if err != nil {
		err = errors.Wrapf(err, "kubectl not found %s", name)
		return
	}
	kargs = append([]string{kubectl}, kargs...)
	return i.Capture(name, true, input, kargs...)
}

// SilentCaptureKubectl calls kubectl and returns its stdout
// without dumping all the output to the logger.
func (i *Installer) SilentCaptureKubectl(name, input string, args ...string) (res string, err error) {
	res = ""
	kargs, err := i.kubeinfo.GetKubectlArray(args...)
	if err != nil {
		err = errors.Wrapf(err, "cluster access for %s", name)
		return
	}
	kubectl, err := i.GetKubectlPath()
	if err != nil {
		err = errors.Wrapf(err, "kubectl not found %s", name)
		return
	}
	kargs = append([]string{kubectl}, kargs...)
	return i.Capture(name, false, input, kargs...)
}

// GetKubectlPath returns the full path to the kubectl executable, or an error if not found
func (i *Installer) GetKubectlPath() (string, error) {
	return exec.LookPath("kubectl")
}

// Capture calls a command and returns its stdout
func (i *Installer) Capture(name string, logToStdout bool, input string, args ...string) (res string, err error) {
	res = ""
	resAsBytes := &bytes.Buffer{}
	i.log.Printf("$ %s", strings.Join(args, " "))
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin = strings.NewReader(input)
	if logToStdout {
		cmd.Stdout = io.MultiWriter(NewLoggingWriter(i.cmdOut), resAsBytes)
	} else {
		cmd.Stdout = resAsBytes
	}
	cmd.Stderr = NewLoggingWriter(i.cmdErr)
	err = cmd.Run()
	if err != nil {
		err = errors.Wrap(err, name)
	}
	res = resAsBytes.String()
	return
}

// Metrics

// SetMetadatum adds a key-value pair to the metrics extra traits field. All
// collected metadata is passed with every subsequent report to Metriton.
func (i *Installer) SetMetadatum(name, key string, value interface{}) {
	i.log.Printf("[Metrics] %s (%q) is %q", name, key, value)
	i.scout.SetMetadatum(key, value)
}

// Report sends an event to Metriton
func (i *Installer) Report(eventName string, meta ...ScoutMeta) {
	i.log.Println("[Metrics]", eventName)
	if err := i.scout.Report(eventName, meta...); err != nil {
		i.log.Println("[Metrics]", eventName, err)
	}
}

func doWordWrap(text string, prefix string, lineWidth int) []string {
	words := strings.Fields(strings.TrimSpace(text))
	if len(words) == 0 {
		return []string{""}
	}
	lines := make([]string, 0)
	wrapped := prefix + words[0]
	for _, word := range words[1:] {
		if len(word)+1 > lineWidth-len(wrapped) {
			lines = append(lines, wrapped)
			wrapped = prefix + word
		} else {
			wrapped += " " + word
		}
	}
	if len(wrapped) > 0 {
		lines = append(lines, wrapped)
	}
	return lines
}

type kubernetesVersion struct {
	Client k8sVersion.Info `json:"clientVersion"`
	Server k8sVersion.Info `json:"serverVersion"`
}

// registration is used to register edgestack.me domains
type registration struct {
	Email            string
	Ip               string
	Hostname         string
	EdgectlInstallId string
	AESInstallId     string
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
