package edgectl

import (
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
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/pkg/browser"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"helm.sh/helm/v3/pkg/strvals"
	k8sTypesMetaV1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sClientCoreV1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"

	"github.com/datawire/ambassador/internal/pkg/edgectl"
	"github.com/datawire/ambassador/pkg/helm"
	"github.com/datawire/ambassador/pkg/k8s"
	"github.com/datawire/ambassador/pkg/metriton"
	"github.com/datawire/ambassador/pkg/supervisor"
)

const (
	// default Helm version rule
	defHelmVersionRule = "*"

	// defInstallNamespace is the default installation namespace
	defInstallNamespace = "ambassador"

	// env variable used for specifying an alternative Helm repo
	// For example, 'https://github.com/datawire/ambassador-chart/archive/BRANCH_NAME.zip'
	// use the GitHub "Clone or download" > "Download ZIP" link
	defEnvVarHelmRepo = "AES_HELM_REPO"

	// env variable used for specifying a SemVer for whitelisting Charts
	// For example, '1.3.*' will install the latest Chart from the Helm repo that installs
	// an image with a '1.3.*' tag.
	defEnvVarChartVersionRule = "AES_CHART_VERSION"

	// env variable used for specifying the image repository (ie, 'quay.io/datawire/aes')
	// this will install the latest Chart from the Helm repo, but with an overridden `image.repository`
	defEnvVarImageRepo = "AES_IMAGE_REPOSITORY"

	// env variable used for overriding the image tag (ie, '1.3.2')
	// this will install the latest Chart from the Helm repo, but with an overridden `image.tag`
	defEnvVarImageTag = "AES_IMAGE_TAG"
)

var (
	// defChartValues defines some default values for the Helm chart
	// see https://github.com/datawire/ambassador-chart#configuration
	defChartValues = map[string]interface{}{
		"replicaCount":           "1",
		"servicePreview.enabled": true,
		"deploymentTool":         "edgectl", // undocumented value, used for setting the "app.kubernetes.io/managed-by"
	}
)

func AESInstall(cmd *cobra.Command, args []string) error {
	skipReport, _ := cmd.Flags().GetBool("no-report")
	verbose, _ := cmd.Flags().GetBool("verbose")
	kcontext, _ := cmd.Flags().GetString("context")
	i := NewInstaller(verbose)

	// If Scout is disabled (environment variable set to non-null), inform the user.
	if metriton.IsDisabledByUser() {
		i.ShowScoutDisabled()
	}

	// Both printed and logged when verbose (Installer.log is responsible for --verbose)
	i.log.Printf("INFO: install_id = %v; trace_id = %v",
		i.scout.Reporter.InstallID(),
		i.scout.Reporter.BaseMetadata["trace_id"])

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
				i.Report("user_interrupted", edgectl.ScoutMeta{"signal", fmt.Sprintf("%+v", sig)})
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
		i.show.Printf("Full logs at %s\n\n", i.logName)
		return runErrors[0]
	}
	return nil
}

// GrabAESInstallID uses "kubectl exec" to ask the AES pod for the cluster's ID,
// which we uses as the AES install ID. This has the side effect of making sure
// the Pod is Running (though not necessarily Ready). This should be good enough
// to report the "deploy" status to metrics.
func (i *Installer) GrabAESInstallID() error {
	aesImage := i.imageRepo + ":" + i.version
	i.log.Printf("> aesImage = %s", aesImage)
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
	clusterID, err := i.kubectl.Exec(podName, containerName, "ambassador", "python3", "kubewatch.py")
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
	serviceInterface := i.coreClient.Services(defInstallNamespace) // namespace
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

// FindMatchingHostResource returns a Host resource from the cluster that
// matches the load balancer's address. The Host resource must refer to a
// *.edgestack.me domain and be in the Ready state.
func (i *Installer) FindMatchingHostResource() (*k8s.Resource, error) {
	client, err := k8s.NewClient(i.kubeinfo)
	if err != nil {
		return nil, err
	}

	resources, err := client.List("Host")
	if err != nil {
		return nil, err
	}

	for _, resource := range resources {
		i.log.Printf("Considering Host %q in ns %q", resource.Name(), resource.Namespace())

		hostname := resource.Spec().GetString("hostname")
		if !strings.HasSuffix(strings.ToLower(hostname), ".edgestack.me") {
			i.log.Printf("--> hostname %q is not a *.edgestack.me address", hostname)
			continue
		}

		state := resource.Status().GetString("state")
		if state != "Ready" {
			i.log.Printf("--> state %q is not Ready", state)
			continue
		}

		if !i.HostnameMatchesLBAddress(hostname) {
			i.log.Printf("--> name does not match load balancer address")
			continue
		}

		i.log.Printf("--> success (hostname is %q)", hostname)
		return &resource, nil
	}

	return nil, nil
}

// HostnameMatchesLBAddress returns whether a *.edgestack.me hostname matches a
// load balancer address, which may be a name or an IP.
func (i *Installer) HostnameMatchesLBAddress(hostname string) bool {
	i.log.Printf("--> Matching hostname %q with LB address %q", hostname, i.address)
	if ip := net.ParseIP(i.address); ip != nil {
		// Address is an IP address, so hostname should have a DNS A (Address)
		// record that points to this IP address
		hostnameIPs, err := net.LookupIP(hostname)
		if err != nil {
			i.log.Printf("    --> hostname IP lookup failed: %+v", err)
			return false
		}
		if len(hostnameIPs) != 1 {
			i.log.Printf("    --> Got %d results instead of 1: %q", len(hostnameIPs), hostnameIPs)
			return false
		}
		if !ip.Equal(hostnameIPs[0]) {
			i.log.Printf("    --> hostname IP %q did not match address IP %q", hostnameIPs[0], ip)
			return false
		}
	} else {
		// Address is a DNS name, so hostname should have a DNS CNAME (Canonical
		// Name) record that points to this DNS name
		cname, err := net.LookupCNAME(hostname)
		if err != nil {
			i.log.Printf("    --> hostname CNAME lookup failed: %+v", err)
			return false
		}
		if !strings.EqualFold(cname, i.address) {
			i.log.Printf("    --> hostname CNAME %q did not match address %q", cname, i.address)
			return false
		}
	}
	i.log.Printf("    --> matched")
	return true
}

// CheckACMEIsDone queries the Host object and succeeds if its state is Ready.
func (i *Installer) CheckACMEIsDone() error {

	host, err := i.kubectl.Get("host", i.hostname, "")
	if err != nil {
		return LoopFailedError(err.Error())
	}
	state, _, err := unstructured.NestedString(host.Object, "status", "state")
	if err != nil {
		return LoopFailedError(err.Error())
	}
	if state == "Error" {
		reason, _, err := unstructured.NestedString(host.Object, "status", "errorReason")
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
		i.ShowACMEFailed(reason)
		return LoopFailedError(fmt.Sprintf("ACME failed. More information: kubectl get host %s -o yaml", i.hostname))
	}
	if state != "Ready" {
		return errors.Errorf("Host state is %s, not Ready", state)
	}
	return nil
}

// CreateNamespace creates the namespace for installing AES
func (i *Installer) CreateNamespace() error {
	_ = i.kubectl.Create("namespace", defInstallNamespace, "")
	// ignore errors: it will fail if the namespace already exists
	// TODO: check that the error message contains "already exists"
	return nil
}

// Perform is the main function for the installer
func (i *Installer) Perform(kcontext string) Result {
	chartValues := map[string]interface{}{}
	for key, value := range defChartValues {
		strvals.ParseInto(fmt.Sprintf("%s=%s", key, value), chartValues)
	}

	// Start
	i.Report("install")

	// Bold: Installing the Ambassador Edge Stack
	i.ShowFirstInstalling()

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
	i.ShowRequestEmail()

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
		return i.resEmailRequestError(errors.New("Interrupted"))
	}

	i.log.Printf("Using email address %q", emailAddress)

	// Beginning the AES Installation
	i.ShowBeginAESInstallation()

	// Attempt to use kubectl
	if _, err = getKubectlPath(); err != nil {
		return i.resNoKubectlError(err)
	}

	// Attempt to talk to the specified cluster
	i.kubeinfo = k8s.NewKubeInfo("", kcontext, "")
	i.kubectl, err = i.NewSimpleKubectl()
	if err != nil {
	}

	if _, err := i.kubectl.ClusterInfo(); err != nil {
		return i.resNoClusterError(err)
	}

	i.restConfig, err = i.kubeinfo.GetRestConfig()

	if err != nil {
		return i.resGetRestConfigError(err)
	}

	i.coreClient, err = k8sClientCoreV1.NewForConfig(i.restConfig)
	if err != nil {
		return i.resNewForConfigError(err)
	}

	i.k8sVersion, err = i.kubectl.WithStdout(ioutil.Discard).Version()
	if err != nil {
		return i.resGetVersionsError(err)
	}

	// Metriton tries to parse fields with `version` in their keys and discards them if it can't.
	// Using _v to keep the version value as string since Kubernetes versions vary in formats.
	i.SetMetadatum("kubectl Version", "kubectl_v", i.k8sVersion.Client.GitVersion)
	i.SetMetadatum("K8s Version", "k8s_v", i.k8sVersion.Server.GitVersion)

	// Try to grab some cluster info
	i.clusterinfo = NewClusterInfo(i.kubectl)
	i.SetMetadatum("Cluster Info", "cluster_info", i.clusterinfo.name)

	// New Helm-based install
	i.FindingRepositoriesAndVersions()

	// Try to verify the existence of an Ambassador deployment in the Ambassador
	// namespace.
	installedVersion, installedInfo, err := getExistingInstallation(i.kubectl)

	if err != nil {
		i.ShowFailedWhenLookingForExistingVersion()
		installedVersion = "" // Things will likely fail when we try to apply manifests
	}

	if installedVersion != "" {
		i.SetMetadatum("Cluster Info", "managed", installedInfo.Name)
		i.ShowAESExistingVersion(installedVersion, installedInfo.LongName)
		i.Report("deploy", edgectl.ScoutMeta{"already_installed", true})

		switch installedInfo.Method {
		case instOSS, instAES, instOperator:
			return i.resCantReplaceExistingInstallationError(installedVersion)
		case instEdgectl, instHelm:
			// if a previous Helm/Edgectl installation has been found MAYBE we can continue with
			// the setup: it depends on the version: continue with the setup and check the version later on
		default:
			// any other case: continue with the rest of the setup
		}
	}

	// the Helm chart heuristics look for the latest release that matches `version_rule`
	version_rule := defHelmVersionRule
	if vr := os.Getenv(defEnvVarChartVersionRule); vr != "" {
		i.ShowOverridingChartVersion(defEnvVarChartVersionRule, vr)
		version_rule = vr
	} else {
		// Allow overriding the image repo and tag
		// This is mutually exclusive with the Chart version rule: it would be too messy otherwise.
		if ir := os.Getenv(defEnvVarImageRepo); ir != "" {
			i.ShowOverridingImageRepo(defEnvVarImageRepo, ir)
			strvals.ParseInto(fmt.Sprintf("image.repository=%s", ir), chartValues)
			i.imageRepo = ir
		} else {
			i.imageRepo = "quay.io/datawire/aes"
		}

		if it := os.Getenv(defEnvVarImageTag); it != "" {
			i.ShowOverridingImageTag(defEnvVarImageTag, it)
			strvals.ParseInto(fmt.Sprintf("image.tag=%s", it), chartValues)
			i.version = it
		}
	}

	// create a new parsed checker for versions
	chartVersion, err := helm.NewChartVersionRule(version_rule)
	if err != nil {
		// this should never happen: it currently breaks only if the version rule ("*") is wrong
		return i.resInternalError(err)
	}

	helmDownloaderOptions := helm.HelmDownloaderOptions{
		Version:  chartVersion,
		Logger:   i.log,
		KubeInfo: i.kubeinfo,
	}
	if u := os.Getenv(defEnvVarHelmRepo); u != "" {
		i.ShowOverridingHelmRepo(defEnvVarHelmRepo, u)
		helmDownloaderOptions.URL = u
	}

	// create a new manager for the remote Helm repo URL
	chartDown, err := helm.NewHelmDownloader(helmDownloaderOptions)
	if err != nil {
		// this should never happen: it currently breaks only if the Helm repo URL cannot be parsed
		return i.resInternalError(err)
	}

	if err := chartDown.Download(); err != nil {
		return i.resDownloadError(err)
	}
	defer func() { _ = chartDown.Cleanup() }()

	if i.version == "" {
		// set the AES version to the version in the Chart we have downloaded
		i.version = strings.Trim(chartDown.GetChart().AppVersion, "\n")
	}

	if installedInfo.Method == instHelm || installedInfo.Method == instEdgectl {
		// if a previous installation was found, check that the installed version matches
		// the downloaded chart version, because we do not support upgrades
		if installedVersion != i.version {
			return i.resCantReplaceExistingInstallationError(installedVersion)
		}
	} else if installedInfo.Method == instNone {
		// nothing was installed: install the Chart
		i.ShowInstalling(i.version)

		err = i.CreateNamespace()
		if err != nil {
			return i.resNamespaceCreationError(err)
		}

		i.clusterinfo.CopyChartValuesTo(chartValues)

		installedRelease, err := chartDown.Install(defInstallNamespace, chartValues)
		if err != nil {
			version := ""
			if installedRelease != nil {
				version = installedRelease.Chart.AppVersion()
			}

			notes := ""
			if ir := os.Getenv("DEBUG"); ir != "" {
				notes = installedRelease.Info.Notes
			}

			// Helm downloader failed
			return i.resFailedToInstallChartError(err, version, notes)
		}

		// record that this cluster is managed with edgectl
		i.SetMetadatum("Cluster Info", "managed", "edgectl")
	}

	// Wait for Ambassador Pod; grab AES install ID
	i.ShowCheckingAESPodDeployment()

	if err := i.loopUntil("AES pod startup", i.GrabAESInstallID, lc2); err != nil {
		return i.resAESPodStartupError(err)
	}
	i.Report("deploy")

	// Don't proceed any further if we know we are using a local (not publicly
	// accessible) cluster. There's no point wasting the user's time on
	// timeouts.

	if i.clusterinfo.isLocal {
		i.ShowLocalClusterDetected()
		i.ShowAESInstallationPartiallyComplete()
		return i.resKnownLocalClusterResult(i.clusterinfo)
	}

	// Grab load balancer address
	i.ShowProvisioningLoadBalancer()

	if err := i.loopUntil("Load Balancer", i.GrabLoadBalancerAddress, lc5); err != nil {
		return i.resLoadBalancerError(err)
	}

	i.Report("cluster_accessible")
	i.ShowAESInstallAddress(i.address)

	// Wait for Ambassador to be ready to serve ACME requests.
	i.ShowAESRespondingToACME()

	if err := i.loopUntil("AES to serve ACME", i.CheckAESServesACME, lc2); err != nil {
		return i.resAESACMEChallengeError(err)
	}
	i.Report("aes_listening")

	if installedVersion != "" {
		i.ShowLookingForExistingHost()
		hostResource, err := i.FindMatchingHostResource()
		if err != nil {
			i.log.Printf("Failed to look for Hosts: %+v", err)
			hostResource = nil
		}
		if hostResource != nil {
			i.hostname = hostResource.Spec().GetString("hostname")
			i.ShowExistingHostFound(hostResource.Name(), hostResource.Namespace())
			i.ShowAESAlreadyInstalled()
			return i.resAESAlreadyInstalledResult()
		}
	}

	i.ShowAESConfiguringTLS()

	// Send a request to acquire a DNS name for this cluster's load balancer
	regURL := "https://metriton.datawire.io/register-domain"
	regData := &registration{Email: emailAddress}

	if !metriton.IsDisabledByUser() {
		regData.AESInstallId = i.clusterID
		regData.EdgectlInstallId = i.scout.Reporter.InstallID()
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
		return i.resDNSNamePostError(err)
	}

	content, err := ioutil.ReadAll(resp.Body)
	resp.Body.Close()

	if err != nil {
		return i.resDNSNameBodyError(err)
	}

	// With and without DNS.  In case of no DNS, different error messages and result handling.
	dnsSuccess := true // Assume success with DNS
	dnsMessage := ""   // Message for error reporting in case of no DNS
	hostName := ""     // Login to this (hostname or IP address)

	// Was there a DNS name post response?
	if resp.StatusCode == 200 {
		// Have DNS name--now wait for it to propagate.
		i.hostname = string(content)
		i.ShowAcquiringDNSName(i.hostname)

		// Wait for DNS to propagate. This tries to avoid waiting for a ten
		// minute error backoff if the ACME registration races ahead of the DNS
		// name appearing for LetsEncrypt.

		if err := i.loopUntil("DNS propagation to this host", i.CheckHostnameFound, lc2); err != nil {
			return i.resDNSPropagationError(err)
		}

		i.Report("dns_name_propagated")

		// Create a Host resource
		hostResource := fmt.Sprintf(hostManifest, i.hostname, i.hostname, emailAddress)
		if err := i.kubectl.Apply(hostResource, ""); err != nil {
			return i.resHostResourceCreationError(err)
		}

		i.ShowObtainingTLSCertificate()

		if err := i.loopUntil("TLS certificate acquisition", i.CheckACMEIsDone, lc5); err != nil {
			return i.resCertificateProvisionError(err)
		}

		i.Report("cert_provisioned")
		i.ShowTLSConfiguredSuccessfully()

		if _, err := i.kubectl.Get("host", i.hostname, ""); err != nil {
			return i.resHostRetrievalError(err)
		}

		// Made it through with DNS and TLS.  Set hostName to the DNS name that was given.
		hostName = i.hostname
		dnsSuccess = true
	} else {
		// Failure case: couldn't create DNS name.  Set hostName the IP address of the host.
		hostName = i.address
		dnsMessage = strings.TrimSpace(string(content))
		i.ShowFailedToCreateDNSName(dnsMessage)
		dnsSuccess = false
	}

	// All done!
	if dnsSuccess {
		i.ShowAESInstallationComplete()
	} else {
		i.ShowAESInstallationCompleteNoDNS()
	}

	// Open a browser window to the Edge Policy Console, with a welcome section or modal dialog.
	if err := edgectl.DoLogin(i.kubeinfo, kcontext, "ambassador", hostName, true, true, false, true); err != nil {
		return i.resAESLoginError(err)
	}

	// Check to see if AES is ready
	if err := i.CheckAESHealth(); err != nil {
		i.Report("aes_health_bad", edgectl.ScoutMeta{"err", err.Error()})
	} else {
		i.Report("aes_health_good")
	}

	// Normal result (with DNS success) or result without DNS.
	if dnsSuccess {
		// Show how to use edgectl login in the future
		return i.resAESInstalledResult(i.hostname)
	} else {
		// Show how to login without DNS.
		return i.resAESInstalledNoDNSResult(resp.StatusCode, dnsMessage, i.address)
	}
}

// Installer represents the state of the installation process
type Installer struct {
	// Cluster

	kubeinfo    *k8s.KubeInfo
	kubectl     Kubectl
	restConfig  *rest.Config
	coreClient  *k8sClientCoreV1.CoreV1Client
	clusterinfo clusterInfo

	// Reporting

	scout *edgectl.Scout

	// Logging and management

	ctx     context.Context
	cancel  context.CancelFunc
	show    *log.Logger
	log     *log.Logger
	cmdOut  *log.Logger
	cmdErr  *log.Logger
	logName string

	// Install results

	k8sVersion KubernetesVersion // cluster version information
	imageRepo  string            // from which docker repo is AES being installed
	version    string            // which AES version is being installed
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
		scout:   edgectl.NewScout("install"),
		ctx:     ctx,
		cancel:  cancel,
		show:    log.New(io.MultiWriter(os.Stdout, logfile), "", 0),
		logName: logfileName,
	}

	if verbose {
		i.log = log.New(io.MultiWriter(logfile, edgectl.NewLoggingWriter(log.New(os.Stderr, "== ", 0))), "", log.Ltime)
		i.cmdOut = log.New(io.MultiWriter(logfile, edgectl.NewLoggingWriter(log.New(os.Stderr, "=- ", 0))), "", 0)
		i.cmdErr = log.New(io.MultiWriter(logfile, edgectl.NewLoggingWriter(log.New(os.Stderr, "=x ", 0))), "", 0)
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

// Metrics

// SetMetadatum adds a key-value pair to the metrics extra traits field. All
// collected metadata is passed with every subsequent report to Metriton.
func (i *Installer) SetMetadatum(name, key string, value interface{}) {
	i.log.Printf("[Metrics] %s (%q) is %q", name, key, value)
	i.scout.SetMetadatum(key, value)
}

// Report sends an event to Metriton
func (i *Installer) Report(eventName string, meta ...edgectl.ScoutMeta) {
	i.log.Println("[Metrics]", eventName)
	if err := i.scout.Report(eventName, meta...); err != nil {
		i.log.Println("[Metrics]", eventName, err)
	}
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
