package main

import (
	"fmt"
	"github.com/pkg/errors"
)

// TODO: Each error listed here of the form *Error() should:
// - report to Metriton the error;
// - Have a reasonable user message;
// - Have a page in our documentation that explains the error and what can be done to resolve it.

// Useful strings, used more than once...
const seeDocsURL = "https://www.getambassador.io/docs/latest/tutorials/getting-started/"
const seeDocs = "See " + seeDocsURL
const tryAgain = "If this appears to be a transient failure, please try running the installer again. It is safe to run the installer repeatedly on a cluster."
const noTlsSuccess = "<bold>You've successfully installed the Ambassador Edge Stack in your Kubernetes cluster. However, we cannot connect to your cluster from the Internet, so we could not configure TLS automatically.</>"

// User interrupted the email request.
func (i *Installer) EmailRequestError(err error) Result {
	return Result{
		URL: "https://www.getambassador.io/docs/topics/install/help/email-request",
		Err: err,
	}
}

// AESInstallMessage occurs here in the sequence.

// Unable to get a kubectl path.
func (i *Installer) NoKubectlError(err error) Result {
	return Result{
		Report:  "fail_no_kubectl",
		Message: "The installer depends on the 'kubectl' executable. Make sure you have the latest release downloaded in your PATH, and that you have executable permissions.",
		URL:     "https://www.getambassador.io/docs/topics/install/help/no-kubectl",
		Err:     err,
	}
}

// Unable to get cluster information
func (i *Installer) NoClusterError(err error) Result {
	noCluster := `
Unable to communicate with the remote Kubernetes cluster using your kubectl context.

To further debug and diagnose cluster problems, use 'kubectl cluster-info dump' 
or get started and run Kubernetes.`

	return Result{
		Report:  "fail_no_cluster",
		URL:     "https://www.getambassador.io/docs/topics/install/help/no-cluster",
		Message: noCluster,
		Err:     err,
	}
}

// Unable to get client configuration or namespace
func (i *Installer) GetRestConfigError(err error) Result {
	return Result{
		Report: "fail_no_cluster",
		URL:    "https://www.getambassador.io/docs/topics/install/help/get-rest-config",
		Err:    err,
	}
}

// Unable to create a new CoreV1Client for the given configuration.
func (i *Installer) NewForConfigError(err error) Result {
	return Result{
		Report: "fail_no_cluster",
		URL:    "https://www.getambassador.io/docs/topics/install/help/new-for-config",
		Err:    err,
	}
}

// Unable to get versions via kubectl
func (i *Installer) GetVersionsError(err error) Result {
	return Result{
		Report: "fail_no_cluster",
		URL:    "https://www.getambassador.io/docs/topics/install/help/get-versions",
		Err:    err,
	}
}

// Unable to fetch the AES CRD manifests (aes-crds.yaml)
func (i *Installer) AESCRDManifestsError(err error) Result {
	i.Report("fail_no_internet", ScoutMeta{"err", err.Error()})

	return Result{
		Message: "download AES CRD manifests",
		URL:     "https://www.getambassador.io/docs/topics/install/help/aes-crd-manifests",
		Err:     errors.Wrap(err, "download AES CRD manifests"),
	}
}

// Unable to fetch the AES manifests (aes.yaml)
func (i *Installer) AESManifestsError(err error) Result {
	i.Report("fail_no_internet", ScoutMeta{"err", err.Error()})

	return Result{
		Message: "download AES manifests",
		URL:     "https://www.getambassador.io/docs/topics/install/help/aes-manifests",
		Err:     errors.Wrap(err, "download AES manifests"),
	}
}

// Unable to parse the downloaded AES manifests
func (i *Installer) ManifestParsingError(err error, matches []string) Result {
	i.log.Printf("matches is %+v", matches)

	return Result{
		Report:  "fail_bad_manifests",
		Message: "Failed to parse downloaded manifests. Is there a proxy server interfering with HTTP downloads?",
		URL:     "https://www.getambassador.io/docs/topics/install/help/manifest-parsing",
		Err:     err,
	}
}

// Existing AES CRD's of incompatible version
func (i *Installer) IncompatibleCRDVersionsError(err error, installedVersion string) Result {
	abortExisting := `
This tool does not support upgrades/downgrades at this time.
The installer will now quit to avoid corrupting an existing installation of AES.
`
	message := fmt.Sprintf("existing AES %s found when installing AES %s\n%s",
		installedVersion, i.version, abortExisting)

	i.Report("fail_existing_aes", ScoutMeta{"installing", i.version}, ScoutMeta{"found", installedVersion})

	return Result{
		URL:     "https://www.getambassador.io/docs/topics/install/help/incompatible-crd-versions",
		Message: message,
		Err:     err,
	}
}

// Existing AES CRD's, unable to upgrade.
func (i *Installer) ExistingCRDsError(err error) Result {
	abortCRDs := `You can manually remove installed CRDs if you are confident they are not in use by any installation. Removing the CRDs will cause your existing Ambassador Mappings and other resources to be deleted as well.

<bold>$ kubectl delete crd -l product=aes</>

The installer will now quit to avoid corrupting an existing (but undetected) installation.
`
	return Result{
		Report:  "fail_existing_crds",
		Message: abortCRDs,
		URL:     "https://www.getambassador.io/docs/topics/install/help/existing-crds",
		Err:     err,
	}
}

// Unable to kubectl apply the aes-crd.yaml manifests
func (i *Installer) InstallCRDsError(err error) Result {
	return Result{
		Report: "fail_install_crds",
		URL:    "https://www.getambassador.io/docs/topics/install/help/install-crds",
		Err:    err,
	}
}

// 90-second timeout on waiting for aes-crd.yaml manifests to be established
func (i *Installer) WaitCRDsError(err error) Result {
	return Result{
		Report: "fail_wait_crds",
		URL:    "https://www.getambassador.io/docs/topics/install/help/wait-crds",
		Err:    err,
	}
}

// Unable to kubectl apply the aes.yaml manifests
func (i *Installer) InstallAESError(err error) Result {
	return Result{
		Report: "fail_install_aes",
		URL:    "https://www.getambassador.io/docs/topics/install/help/install-aes",
		Err:    err,
	}
}

//90-second timeout on waiting for aes.yaml manifests to be deployed and available
func (i *Installer) WaitForAESError(err error) Result {
	return Result{
		Report: "fail_wait_aes",
		URL:    "https://www.getambassador.io/docs/topics/install/help/wait-for-aes",
		Err:    err,
	}
}

// Unable to get the AES Install ID via kubectl exec to ask for the pod ID
func (i *Installer) AESPodStartupError(err error) Result {
	return Result{
		Report: "fail_pod_timeout",
		URL:    "https://www.getambassador.io/docs/topics/install/help/aes-pod-startup",
		Err:    err,
	}
}

// docker-desktop, minikube, or kind: local cluster so no automatic TLS.
func (i *Installer) KnownLocalClusterResult() Result {
	message := noTlsSuccess
	message += "\n\n"
	message += "Determine the IP address and port number of your Ambassador service, e.g.\n"
	message += "<bold>$ minikube service -n ambassador ambassador</>\n\n"
	message += "The following command will open the Edge Policy Console once you accept a self-signed certificate in your browser.\n"
	message += "<bold>$ edgectl login -n ambassador IP_ADDRESS:PORT</>"
	message += "\n\n"

	return Result{
		Report: "cluster_not_accessible",
		Message: message,
		URL:    seeDocsURL,
		Err:    nil,
	}
}

// Unable to provision a load balancer (failed to retrieve the IP address)
func (i *Installer) LoadBalancerError(err error) Result {
	message := noTlsSuccess
	message += "\n\n"
	message += `We timed out waiting for the load balancer's IP address for the AES Service.
- If a load balancer IP address shows up, simply run the installer again.
- If your cluster doesn't support load balancers, you'll need to expose AES some other way.`
	message += "\n\n"

	return Result{

		Report: "fail_loadbalancer_timeout",
		Message: message,
		URL:    "https://www.getambassador.io/docs/topics/install/help/load-balancer",
		Err:    err,
	}
}

// AES failed to respond to the ACME challenge.  This may be because AES did not start quickly enough or
// if the AES load balancer is not reachable.
func (i *Installer) AESACMEChallengeError(err error) Result {
	message := "<bold>It seems AES did not start in the expected time, or the AES load balancer is not reachable from here.</>"
	message += "\n\n"
	message += tryAgain
	message += "\n\n"

	return Result{
		Report:   "aes_listening_timeout",
		Message: message,
		TryAgain: true,
		URL: "https://www.getambassador.io/docs/topics/install/help/aes-acme-challenge",
		Err: err,
	}
}

// Unable to make an HTTP Post to Metriton at https://metriton.datawire.io/register-domain
// and so cannot acquire a DNS name for the cluster's load balancer.
func (i *Installer) DNSNamePostError(err error) Result {
	i.Report("dns_name_failure", ScoutMeta{"err", err.Error()})

	return Result{
		URL: "https://www.getambassador.io/docs/topics/install/help/dns-name-post",
		Err: errors.Wrap(err, "acquire DNS name (post)"),
	}
}

// Unable to fetch the response from the HTTP Post to Metriton.
func (i *Installer) DNSNameBodyError(err error) Result {
	i.Report("dns_name_failure", ScoutMeta{"err", err.Error()})

	return Result{
		Message: "acquire DNS name (read body)",
		URL:     "https://www.getambassador.io/docs/topics/install/help/dns-name-body",
		Err:     errors.Wrap(err, "acquire DNS name (read body)"),
	}
}

// Successful installation but no DNS.
func (i *Installer) AESInstalledNoDNSResult(statusCode int, message string) Result {
	i.Report("dns_name_failure", ScoutMeta{"code", statusCode}, ScoutMeta{"err", message})

	success := `
<bold>Congratulations! You've successfully installed the Ambassador Edge Stack in your Kubernetes cluster. However, we cannot connect to your cluster from the Internet, so we could not configure TLS automatically.</>

If this IP address is reachable from here, you can access your installation without a DNS name. The following command will open the Edge Policy Console once you accept a self-signed certificate in your browser.
<bold>$ edgectl login -n ambassador {{ .address }}</>

You can use port forwarding to access your Edge Stack installation and the Edge Policy Console.  You will need to accept a self-signed certificate in your browser.
<bold>$ kubectl -n ambassador port-forward deploy/ambassador 8443 &</>
<bold>$ edgectl login -n ambassador 127.0.0.1:8443</>
`
	return Result{
		Message: success,
		URL:     seeDocsURL,
		Report:  "", // FIXME: reported above due to additional metadata required
	}
}

// The DNS name propagation timed out, so unable to resolve the name.
func (i *Installer) DNSPropagationError(err error) Result {
	return Result{
		Report:   "dns_name_propagation_timeout",
		Message:  "We are unable to resolve your new DNS name on this machine." + seeDocs + tryAgain,
		TryAgain: true,
		URL:      "https://www.getambassador.io/docs/topics/install/help/dns-propagation",
		Err:      err,
	}
}

// In attempting to kubectl apply the hostResource yaml, kubectl failed.
func (i *Installer) HostResourceCreationError(err error) Result {
	i.Report("fail_host_resource", ScoutMeta{"err", err.Error()})

	return Result{
		Message: "We failed to create a Host resource in your cluster. This is unexpected." + seeDocs,
		URL:     "https://www.getambassador.io/docs/topics/install/help/host-resource-creation",
		Err:     err,
	}
}

// Unable to acquire a TLS certificate from Let's Encrypt
func (i *Installer) CertificateProvisionError(err error) Result {

	return Result{
		Report:   "cert_provision_failed",
		Message:  seeDocs + tryAgain,
		TryAgain: true,
		URL:      "https://www.getambassador.io/docs/topics/install/help/certificate-provision",
		Err:      err,
	}
}

// Unable to acquire a TLS certificate from Let's Encrypt
func (i *Installer) HostRetrievalError(err error) Result {
	return Result{
		Message:  "We failed to retrieve the Host resource from your cluster that we just created. This is unexpected." + tryAgain,
		TryAgain: true,
		URL:      "https://www.getambassador.io/docs/topics/install/help/host-retrieval",
		Err:      err,
	}
}

// AESInstallCompleteMessage occurs here in the sequence.

// Attempted to log in to the cluster but failed.
func (i *Installer) AESLoginError(err error) Result {
	return Result{
		URL: "https://www.getambassador.io/docs/topics/install/help/aes-login",
		Err: err,
	}
}

// AES login successful!
func (i *Installer) AESLoginSuccessResult() Result {
	return Result{
		Err: nil,
	}
}
