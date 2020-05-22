package edgectl

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"log"
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
	k8sClientCoreV1 "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/datawire/ambassador/internal/pkg/edgectl"
	"github.com/datawire/ambassador/pkg/helm"
	"github.com/datawire/ambassador/pkg/k8s"
	"github.com/datawire/ambassador/pkg/metriton"
	"github.com/datawire/ambassador/pkg/supervisor"
)

var (
	errUpgradeNoAmbInst = errors.New("no AmbassadorInstallation resource found")

	errUpgradeAmbInstNotInstalled = errors.New("AmbassadorInstallation found, but Ambassador API Gateway not installed")

	errUpgradeAmbInstNotInstallOSS = errors.New("AmbassadorInstallation does not have 'installOSS: True'")

	errUpgradeAmbInstFlavorAES = errors.New("the AmbassadorInstallation already seems to be an AES installation")
)

func AOSSUpgrade(cmd *cobra.Command, args []string) error {
	skipReport, _ := cmd.Flags().GetBool("no-report")
	verbose, _ := cmd.Flags().GetBool("verbose")
	kcontext, _ := cmd.Flags().GetString("context")
	i := NewUpgrader(verbose)

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
		Name:     "upgrade",
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

type Upgrader struct {
	Installer
}

// NewUpgrader returns an Installer object after setting up logging.
func NewUpgrader(verbose bool) *Upgrader {
	// Although log, cmdOut, and cmdErr *can* go to different files and/or have
	// different prefixes, they'll probably all go to the same file, possibly
	// with different prefixes, for most cases.
	logfileName := filepath.Join(os.TempDir(), time.Now().Format("edgectl-upgrade-20060102-150405.log"))
	logfile, err := os.Create(logfileName)
	if err != nil {
		logfile = os.Stderr
		fmt.Fprintf(logfile, "WARNING: Failed to open logfile %q: %+v\n", logfileName, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	i := Upgrader{
		Installer{
			scout:   edgectl.NewScout("upgrade"),
			ctx:     ctx,
			cancel:  cancel,
			show:    log.New(io.MultiWriter(os.Stdout, logfile), "", 0),
			logName: logfileName,
		},
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

	return &i
}

// Perform is the main function for the upgrader
func (i *Upgrader) Perform(kcontext string) Result {
	var err error

	chartValues := map[string]interface{}{}
	for key, value := range defChartValues {
		strvals.ParseInto(fmt.Sprintf("%s=%s", key, value), chartValues)
	}

	// Start
	i.Report("upgrade")

	// Bold: Upgrading the Ambassador AP{I Gateway to Edge Stack
	i.ShowFirstUpgrading()

	emailAddress, result := i.AskEmail()
	if result.Err != nil {
		return result
	}

	// Beginning the AOSS Upgrade
	i.ShowBeginAOSSUpgrade()

	// Attempt to use kubectl
	if _, err = getKubectlPath(); err != nil {
		return i.resNoKubectlError(err)
	}

	// Attempt to talk to the specified cluster
	i.kubeinfo = k8s.NewKubeInfo("", kcontext, "")
	i.kubectl, err = i.NewSimpleKubectl()
	if err != nil {
		return i.resGetRestConfigError(err)
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

	// Try to grab some cluster info.
	i.clusterinfo = NewClusterInfo(i.kubectl)
	i.SetMetadatum("Cluster Info", "cluster_info", i.clusterinfo.name)

	// Check that there is an AmbassadorInstallation
	ambInstallation, err := findAmbassadorInstallation(i.kubectl)
	if err != nil {
		return i.resUpgradeNoOSSFound(err)
	}
	if ambInstallation == nil || ambInstallation.IsEmpty() {
		return i.resUpgradeNoOSSFound(errUpgradeNoAmbInst)
	}
	if !ambInstallation.IsInstalled() {
		return i.resUpgradeNoOSSFound(errUpgradeAmbInstNotInstalled)
	}

	// Check that the installation is an OSS installation
	// we check both that user wanted OSS AND use has OSS
	// otherwise, we could mess up installations that are in-progress...
	flavor, err := ambInstallation.GetFlavor()
	if err != nil {
		return i.resUpgradeNoOSSFound(err)
	}
	if flavor == flavorAES {
		return i.resUpgradeNoOSSFound(errUpgradeAmbInstFlavorAES)
	}
	if !ambInstallation.GetInstallOSS() {
		return i.resUpgradeNoOSSFound(errUpgradeAmbInstNotInstallOSS)
	}

	// Check that the OSS version installed is the latest one
	// create a downloader and look for the latest version in the repo
	i.FindingRepositoriesAndVersions()
	chartVersion, err := helm.NewChartVersionRule(defHelmVersionRule)
	if err != nil {
		// this should never happen: it currently breaks only if the version rule ("*") is wrong
		return i.resInternalError(err)
	}

	// Allow overriding the image repo and tag
	if ir := os.Getenv(defEnvVarImageRepo); ir != "" {
		i.ShowOverridingImageRepo(defEnvVarImageRepo, ir)
		if err := strvals.ParseInto(fmt.Sprintf("image.repository=%s", ir), chartValues); err != nil {
			return i.resInternalError(err)
		}
		i.imageRepo = ir
	} else {
		i.imageRepo = defImageRepo
	}

	if it := os.Getenv(defEnvVarImageTag); it != "" {
		i.ShowOverridingImageTag(defEnvVarImageTag, it)
		if err := strvals.ParseInto(fmt.Sprintf("image.tag=%s", it), chartValues); err != nil {
			return i.resInternalError(err)
		}
		i.version = it
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

	// Create a new manager for the remote Helm repo URL
	chartDown, err := helm.NewHelmDownloader(helmDownloaderOptions)
	if err != nil {
		// this should never happen: it currently breaks only if the Helm repo URL cannot be parsed
		return i.resInternalError(err)
	}

	installedVersion, err := ambInstallation.GetInstalledVersion()
	if err != nil {
		return i.resDownloadError(err)
	}
	latestVersionAvailable, err := chartDown.FindLatestVersionAvailable()
	if err != nil {
		return i.resDownloadError(err)
	}
	defer func() { _ = chartDown.Cleanup() }()

	moreRecent, err := helm.MoreRecentThan(latestVersionAvailable, installedVersion)
	if err != nil || moreRecent {
		return i.resUpgradeTooOldOSSError(installedVersion, latestVersionAvailable)
	}

	if i.version == "" {
		i.version = strings.Trim(latestVersionAvailable, "\n")
	}

	// Perform the upgrade: remove the `installOSS`
	if err := ambInstallation.SetInstallOSS(false); err != nil {
		return i.resUpgradeFailed(err)
	}

	// ... re-apply the AmbassadorInstallation, but with installOSS=false
	i.ShowUpgrading(latestVersionAvailable)
	content, err := ambInstallation.MarshalJSON()
	if err != nil {
		return i.resUpgradeFailed(err)
	}
	if err := i.kubectl.Apply(string(content), defInstallNamespace); err != nil {
		return i.resUpgradeApplyError(err)
	}

	// ... and wait for the operator to do its work and the AmbassadorInstallation
	// to have GetFlavor() == AES
	err = i.loopUntil("Upgrade", func() error { return checkAmbInstWithFlavor(i.kubectl, flavorAES) }, lc5)
	if err != nil {
		// try to get some descriptive error from the last "condition" in the AmbassadorInstallation
		if ambInstallation, err := findAmbassadorInstallation(i.kubectl); err == nil {
			reason, message := ambInstallation.LastConditionExplain()
			if strings.Contains(reason, "Error") {
				return i.resUpgradeFailed(errors.New(message))
			}
		}
		// we could not get a reason: just show a generic error
		return i.resUpgradeFailed(err)
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

	dnsMessage, hostName, resp, res := i.ConfigureTLS(emailAddress)
	if res.Err != nil {
		return result
	}

	// All done!
	dnsSuccess := hostName == i.hostname
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
		i.Report("aes_health_bad",
			edgectl.ScoutMeta{"err", err.Error()})
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
