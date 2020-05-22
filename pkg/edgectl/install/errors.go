package edgectl

import (
	"fmt"

	"github.com/gookit/color"
	"github.com/pkg/errors"

	"github.com/datawire/ambassador/internal/pkg/edgectl"
)

// Each error listed here of the form *Error() should:
// - report to Metriton the error;
// - Have a reasonable user message;
// - Have a page in our documentation that explains the error and what can be done to resolve it.

// Useful string, used more than once...
const noTlsSuccess = "<bold>You've successfully installed the Ambassador Edge Stack in your Kubernetes cluster. However, we cannot connect to your cluster from the Internet, so we could not configure TLS automatically.</>"

// An internal error that should never happen.
func (i *Installer) resInternalError(err error) Result {
	url := "https://www.getambassador.io/docs/latest/tutorials/getting-started/"

	return Result{
		ShortMessage: "The installer experienced an internal error",
		Message:      fmt.Sprintf("Find a more detailed explanation and step-by-step instructions about how to continue installing Ambassador Edge Stack at %v", url),
		URL:          url,
		Err:          err,
	}
}

// User interrupted the email request.
func (i *Installer) resEmailRequestError(err error) Result {
	url := "https://www.getambassador.io/docs/latest/topics/install/help/email-request"

	return Result{
		ShortMessage: "The request for email was interrupted or failed.",
		Message:      fmt.Sprintf("Find a more detailed explanation and step-by-step instructions about how to continue installing Ambassador Edge Stack at %v", url),
		URL:          url,
		Err:          err,
	}
}

// AESInstallMessage occurs here in the sequence.

// Unable to get a kubectl path.
func (i *Installer) resNoKubectlError(err error) Result {
	url := "https://www.getambassador.io/docs/latest/topics/install/help/no-kubectl"

	return Result{
		Report:       "fail_no_kubectl",
		ShortMessage: "The installer was unable to find kubectl in your $PATH",
		Message:      fmt.Sprintf("Find a more detailed explanation and step-by-step instructions about how to install and configure kubectl to continue installing Ambassador Edge Stack at %v", url),
		URL:          url,
		Err:          err,
	}
}

// Unable to get cluster information
func (i *Installer) resNoClusterError(err error) Result {
	url := "https://www.getambassador.io/docs/latest/topics/install/help/no-cluster"

	return Result{
		Report:       "fail_no_cluster",
		URL:          url,
		ShortMessage: "The installer could not find a Kubernetes cluster",
		Message:      fmt.Sprintf("Find a more detailed explanation and step-by-step instructions about how to set up your Kubernetes environment to continue installing Ambassador Edge Stack at %v", url),
		Err:          err,
	}
}

// Unable to get client configuration or namespace
func (i *Installer) resGetRestConfigError(err error) Result {
	url := "https://www.getambassador.io/docs/latest/topics/install/help/get-rest-config"

	return Result{
		Report:       "fail_no_cluster",
		ShortMessage: "The installer could not communicate with your Kubernetes cluster",
		Message:      fmt.Sprintf("Find a more detailed explanation and step-by-step instructions about how to set up your Kubernetes environment to continue installing Ambassador Edge Stack at %v", url),
		URL:          url,
		Err:          err,
	}
}

// Unable to create a new CoreV1Client for the given configuration.
func (i *Installer) resNewForConfigError(err error) Result {
	url := "https://www.getambassador.io/docs/latest/topics/install/help/new-for-config"

	return Result{
		Report:       "fail_no_cluster",
		ShortMessage: "The installer could not communicate with your Kubernetes cluster",
		Message:      fmt.Sprintf("Find a more detailed explanation and step-by-step instructions about how to set up your Kubernetes environment to continue installing Ambassador Edge Stack at %v", url),
		URL:          url,
		Err:          err,
	}
}

// Unable to get versions via kubectl
func (i *Installer) resGetVersionsError(err error) Result {
	url := "https://www.getambassador.io/docs/latest/topics/install/help/get-versions"

	return Result{
		Report:       "fail_no_cluster",
		ShortMessage: "The installer could not communicate with your Kubernetes cluster",
		Message:      fmt.Sprintf("Find a more detailed explanation and step-by-step instructions about how to set up your Kubernetes environment to continue installing Ambassador Edge Stack at %v", url),
		URL:          url,
		Err:          err,
	}
}

// Unable to download the latest Chart
func (i *Installer) resDownloadError(err error) Result {
	i.Report("fail_no_internet", edgectl.ScoutMeta{"err", err.Error()})

	url := "https://www.getambassador.io/docs/latest/topics/install/help/download-error"

	return Result{
		ShortMessage: "The installer failed to download AES",
		Message:      fmt.Sprintf("Find a more detailed explanation and step-by-step instructions about downloading the AES Chart to continue installing Ambassador Edge Stack at %v", url),
		URL:          url,
		Err:          errors.Wrap(err, "Failed to download AES"),
	}
}

func (i *Installer) resCantReplaceExistingInstallationError(installedVersion string) Result {
	url := "https://www.getambassador.io/docs/latest/topics/install/help/existing-installation"

	return Result{
		Report:       "fail_existing_installation",
		ShortMessage: "The installer is unable to replace an existing installation",
		Message:      fmt.Sprintf("Find a more detailed explanation and step-by-step instructions about removing an existing installation to continue installing Ambassador Edge Stack at %v", url),
		URL:          url,
		Err:          errors.New("Can't replace existing installation"),
	}
}

func (i *Installer) resUpgradeTooOldOSSError(installedVersion, latestVersion string) Result {
	url := "https://www.getambassador.io/docs/latest/topics/install/help/upgrade-tool-old"

	return Result{
		Report: "fail_old_oss_installation",
		ShortMessage: fmt.Sprintf("The upgrader is unable to upgrade an API Gateway %s installation to Edge Stack %s",
			installedVersion, latestVersion),
		Message: fmt.Sprintf("Find a more detailed explanation and step-by-step instructions about upgrading an existing installation to continue upgrading Ambassador API Gateway to Edge Stack at %v", url),
		URL:     url,
		Err:     errors.New(fmt.Sprintf("Can't upgrade an OSS %s installation to AES %s", installedVersion, latestVersion)),
	}
}

// resUpgradeNoOSSFound returns an error when OSS was not found
func (i *Installer) resUpgradeNoOSSFound(err error) Result {
	url := "https://www.getambassador.io/docs/latest/topics/install/help/upgrade-no-oss-found"
	if err == nil {
		err = errUpgradeNoAmbInst
	}

	return Result{
		Report:       "fail_no_oss_found",
		ShortMessage: fmt.Sprintf("The upgrader is unable to find a valid Ambassador API Gateway installation: %s.", err),
		Message:      fmt.Sprintf("Find a more detailed explanation and step-by-step instructions about upgrading an existing installation to continue upgrading Ambassador API Gateway to Edge Stack at %v", url),
		URL:          url,
		Err:          err,
	}
}

// resUpgradeApplyError returns an error when applying the new manifest
func (i *Installer) resUpgradeApplyError(err error) Result {
	url := "https://www.getambassador.io/docs/latest/topics/install/help/upgrade-apply-error"

	return Result{
		Report:       "fail_upgrade_apply",
		ShortMessage: "The installer could not update the AmbassadorInstallation manifest in your cluster",
		Message:      fmt.Sprintf("Find a more detailed explanation and step-by-step instructions about how to set up your Kubernetes environment to continue installing Ambassador Edge Stack at %v", url),
		URL:          url,
		Err:          err,
	}
}

// resUpgradeFailed returns a generic error on upgrades
func (i *Installer) resUpgradeFailed(err error) Result {
	url := "https://www.getambassador.io/docs/latest/topics/install/help/upgrade-error"

	return Result{
		Report:       "fail_upgrade",
		ShortMessage: fmt.Sprintf("The upgrader failed to install Ambassador Edge Stack: %s.", err),
		Message:      fmt.Sprintf("Find a more detailed explanation and step-by-step instructions about upgrading an existing installation to continue upgrading Ambassador API Gateway to Edge Stack at %v", url),
		URL:          url,
		Err:          err,
	}
}

func (i *Installer) resNamespaceCreationError(err error) Result {
	i.Report("fail_install_aes", edgectl.ScoutMeta{"err", err.Error()})
	url := "https://www.getambassador.io/docs/latest/topics/install/help/install-aes"

	return Result{
		Report:       "fail_install_aes",
		ShortMessage: "Namespace creation failed while installing AES",
		Message:      fmt.Sprintf("Find a more detailed explanation and suggestions on how to continue installing Ambassador Edge Stack at %v", url),
		URL:          url,
		Err:          err,
	}
}

func (i *Installer) resFailedToInstallChartError(err error, version string, notes string) Result {
	url := "https://www.getambassador.io/docs/latest/topics/install/help/install-aes"
	i.Report("fail_install_aes", edgectl.ScoutMeta{"err", err.Error()})

	msg := fmt.Sprintf("Failed to install Helm chart: %s", err)

	if version != "" {
		msg += "\n\n"
		msg += version
	}

	if notes != "" {
		msg += "\n\n"
		msg += notes
	}

	// TODO: decide what to do with the composed message.  It's too long for a ShortMessage and too detailed
	// TODO: for a Message.  Ideally this information should be in a dedicated documentation page.

	return Result{
		Report:       "fail_install_aes",
		ShortMessage: "Failed to install Helm chart",
		Message:      fmt.Sprintf("Find a more detailed explanation and suggestions on how to continue installing Ambassador Edge Stack at %v", url),
		URL:          url,
		Err:          err,
	}
}

// Unable to kubectl apply the aes.yaml manifests
func (i *Installer) resInstallAESError(err error) Result {
	url := "https://www.getambassador.io/docs/latest/topics/install/help/install-aes"

	return Result{
		Report:       "fail_install_aes",
		ShortMessage: "An error occurred while installing AES",
		Message:      fmt.Sprintf("Find a more detailed explanation and suggestions on how to continue installing Ambassador Edge Stack at %v", url),
		URL:          url,
		Err:          err,
	}
}

// Unable to get the AES Install ID via kubectl exec to ask for the pod ID
func (i *Installer) resAESPodStartupError(err error) Result {
	url := "https://www.getambassador.io/docs/latest/topics/install/help/aes-pod-startup"

	return Result{
		Report:       "fail_pod_timeout",
		ShortMessage: "The installer was unable to talk to your AES pod",
		Message:      fmt.Sprintf("Find a more detailed explanation and step-by-step instructions about resolving this problem at %v", url),
		URL:          url,
		Err:          err,
	}
}

// docker-desktop, minikube, or kind: local cluster so no automatic TLS.
func (i *Installer) resKnownLocalClusterResult(ci clusterInfo) Result {
	url := "https://www.getambassador.io/docs/latest/tutorials/getting-started/"

	getServiceIPmsg := "kubectl get services -n ambassador ambassador"
	if ci.customMessages.getServiceIP != "" {
		getServiceIPmsg = ci.customMessages.getServiceIP
	}

	message := noTlsSuccess
	message += "\n\n"
	message += "Determine the IP address and port number of your Ambassador service, e.g.\n"
	message += fmt.Sprintf("<bold>$ %s </>\n\n", getServiceIPmsg)
	message += "The following command will open the Edge Policy Console once you accept a self-signed certificate in your browser.\n"
	message += "<bold>$ edgectl login -n ambassador IP_ADDRESS:PORT</>"
	message += "\n\n"

	return Result{
		Report:  "cluster_not_accessible",
		Message: message,
		URL:     url,
		Err:     nil,
	}
}

// Unable to provision a load balancer (failed to retrieve the IP address)
func (i *Installer) resLoadBalancerError(err error) Result {
	url := "https://www.getambassador.io/docs/latest/topics/install/help/load-balancer"

	message := noTlsSuccess
	message += "\n\n"
	message += fmt.Sprintf("Find a more detailed explanation and step-by-step instructions about exposing a public load balancer to complete the Ambassador Edge Stack installation at %v", url)
	message += "\n\n"

	return Result{
		Report:       "fail_loadbalancer_timeout",
		ShortMessage: "The installer timed out waiting for the load balancer's IP address for the AES Service",
		Message:      message,
		URL:          url,
		Err:          err,
	}
}

// AES failed to respond to the ACME challenge.  This may be because AES did not start quickly enough or
// if the AES load balancer is not reachable.
func (i *Installer) resAESACMEChallengeError(err error) Result {
	url := "https://www.getambassador.io/docs/latest/topics/install/help/aes-acme-challenge"

	message := "<bold>It seems AES did not start in the expected time, or the AES load balancer is not reachable from here.</>"
	message += "\n\n"
	message += fmt.Sprintf("Find a more detailed explanation and step-by-step instructions about completing the ACME challenge to finish installing Ambassador Edge Stack at %v\n", url)

	return Result{
		Report:       "aes_listening_timeout",
		ShortMessage: "The Ambassador Edge Stack failed to respond to the ACME challenge.",
		Message:      message,
		TryAgain:     true,
		URL:          url,
		Err:          err,
	}
}

// Unable to make an HTTP Post to Metriton at https://metriton.datawire.io/register-domain
// and so cannot acquire a DNS name for the cluster's load balancer.
func (i *Installer) resDNSNamePostError(err error) Result {
	url := "https://www.getambassador.io/docs/latest/topics/install/help/dns-name-post"
	i.Report("dns_name_failure", edgectl.ScoutMeta{"err", err.Error()})

	return Result{
		ShortMessage: "Failed to register DNS name for the current installation",
		Message:      fmt.Sprintf("Find a more detailed explanation and step-by-step instructions about acquiring a DNS name to continue installing Ambassador Edge Stack at %v", url),
		URL:          url,
		Err:          errors.Wrap(err, "Failed to acquire DNS name (post)"),
	}
}

// Unable to fetch the response from the HTTP Post to Metriton.
func (i *Installer) resDNSNameBodyError(err error) Result {
	url := "https://www.getambassador.io/docs/latest/topics/install/help/dns-name-body"
	i.Report("dns_name_failure", edgectl.ScoutMeta{"err", err.Error()})

	return Result{
		ShortMessage: "Failed to register DNS name for the current installation",
		Message:      fmt.Sprintf("Find a more detailed explanation and step-by-step instructions about acquiring a DNS name to continue installing Ambassador Edge Stack at %v", url),
		URL:          url,
		Err:          errors.Wrap(err, "Failed to acquire DNS name (read body)"),
	}
}

// The DNS name propagation timed out, so unable to resolve the name.
func (i *Installer) resDNSPropagationError(err error) Result {
	url := "https://www.getambassador.io/docs/latest/topics/install/help/dns-propagation"

	return Result{
		Report:       "dns_name_propagation_timeout",
		ShortMessage: "The installer was unable to resolve your new DNS name on this machine",
		Message:      fmt.Sprintf("Find a more detailed explanation and step-by-step instructions about acquiring and resolving a DNS name to continue installing Ambassador Edge Stack at %v", url),
		TryAgain:     true,
		URL:          url,
		Err:          err,
	}
}

// In attempting to kubectl apply the hostResource yaml, kubectl failed.
func (i *Installer) resHostResourceCreationError(err error) Result {
	url := "https://www.getambassador.io/docs/latest/topics/install/help/host-resource-creation"
	i.Report("fail_host_resource", edgectl.ScoutMeta{"err", err.Error()})

	return Result{
		ShortMessage: "The installer failed to create a Host resource in your cluster. This is unexpected.",
		Message:      fmt.Sprintf("Find a more detailed explanation and step-by-step instructions about creating a Host resource to continue installing Ambassador Edge Stack at %v", url),
		URL:          url,
		Err:          err,
	}
}

// Unable to acquire a TLS certificate from Let's Encrypt
func (i *Installer) resCertificateProvisionError(err error) Result {
	url := "https://www.getambassador.io/docs/latest/topics/install/help/certificate-provision"

	return Result{
		Report:       "cert_provision_failed",
		ShortMessage: "The installer was unable to acquire a TLS certificate from Let's Encrypt",
		Message:      fmt.Sprintf("Find a more detailed explanation and step-by-step instructions about provisioning a TLS certificate to continue installing Ambassador Edge Stack at %v", url),
		TryAgain:     true,
		URL:          url,
		Err:          err,
	}
}

// Unable to acquire a TLS certificate from Let's Encrypt
func (i *Installer) resHostRetrievalError(err error) Result {
	url := "https://www.getambassador.io/docs/latest/topics/install/help/host-retrieval"

	return Result{
		ShortMessage: "The installer failed to retrieve the Host resource from your cluster that was just created. This is unexpected.",
		Message:      fmt.Sprintf("Find a more detailed explanation and step-by-step instructions about retrieving the Host resource to continue installing Ambassador Edge Stack at %v", url),
		TryAgain:     true,
		URL:          url,
		Err:          err,
	}
}

// AESInstallCompleteMessage occurs here in the sequence.

// Attempted to log in to the cluster but failed.
func (i *Installer) resAESLoginError(err error) Result {
	url := "https://www.getambassador.io/docs/latest/topics/install/help/aes-login"

	message := "The installer failed to log in to the Ambassador Edge Policy Console.\n\n"
	message += fmt.Sprintf("Find a more detailed explanation and suggestions on how to resolve this problem at %v", url)

	return Result{
		Message: message,
		URL:     url,
		Err:     nil,
	}
}

// Successful installation but no DNS.
func (i *Installer) resAESInstalledNoDNSResult(statusCode int, dnsMessage string, hostIP string) Result {
	i.Report("dns_name_failure", edgectl.ScoutMeta{"code", statusCode}, edgectl.ScoutMeta{"err", dnsMessage})

	message := "In the future, the following command will log in to the Ambassador Edge Policy Console once you accept a self-signed certificate in your browser.\n"
	message += color.Bold.Sprintf("$ edgectl login " + hostIP)

	return Result{
		Message: message,
		Err:     nil,
	}
}

// AES login successful!
func (i *Installer) resAESInstalledResult(hostname string) Result {
	message := "In the future, the following command will log in to the Ambassador Edge Policy Console.\n"
	message += color.Bold.Sprintf("$ edgectl login " + hostname)

	return Result{
		Message: message,
		Err:     nil,
	}
}

// AES already installed. Suggest using "edgectl login" instead.
func (i *Installer) resAESAlreadyInstalledResult() Result {
	message := "Use the following command to open the Ambassador Edge Policy Console:\n"
	message += color.Bold.Sprintf("$ edgectl login {{.hostname}}")

	return Result{
		Message: message,
		Err:     nil,
	}
}
