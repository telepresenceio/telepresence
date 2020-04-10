package main

import (
	"fmt"

	"github.com/gookit/color"
)

// First message when beginning the AES Installation process
func (i *Installer) ShowFirstInstalling() {
	i.show.Println()
	i.show.Println(color.Bold.Sprintf("Installing the Ambassador Edge Stack"))
}

func (i *Installer) ShowScoutDisabled() {
	i.show.Printf("INFO: phone-home is disabled by environment variable")
}

func (i *Installer) ShowRequestEmail() {
	i.show.Println()
	i.ShowWrapped("Please enter an email address for us to notify you before your TLS certificate and domain name expire. In order to acquire the TLS certificate, we share this email with Let’s Encrypt.")
}

func (i *Installer) ShowACMEFailed(reason string) {
	i.show.Println()
	i.show.Println(color.Bold.Sprintf("Acquiring TLS certificate via ACME has failed: %s", reason))
}

func (i *Installer) ShowBeginAESInstallation() {
	i.show.Println()
	i.show.Println("========================================================================")
	i.show.Println("Beginning Ambassador Edge Stack Installation")
}

func (i *Installer) ShowFailedToLookForExistingVersion(err error) {
	i.show.Println("Failed to look for an existing installation:", err)
}

func (i *Installer) ShowAESVersionBeingInstalled() {
	i.show.Println(fmt.Sprintf("-> Installing the Ambassador Edge Stack %s.", i.version))
}

func (i *Installer) ShowAESExistingVersion(versionName string, method string) {
	i.show.Println(fmt.Sprintf("   Ambassador Edge Stack %s already installed with %s", versionName, method))
}

func (i *Installer) ShowAESInstalledByHelm() {
	i.ShowWrapped("-> Ambassador was installed with Helm")
}

func (i *Installer) ShowOverridingChartVersion(aesChartVersion string, version string) {
	i.ShowWrapped(fmt.Sprintf("   Overriding Chart version rule from %q: %s.", aesChartVersion, version))
}

func (i *Installer) ShowOverridingImageRepo(aesImageRepo string, repo string) {
	i.ShowWrapped(fmt.Sprintf("   Overriding image repo from %q: %s.", aesImageRepo, repo))
}

func (i *Installer) ShowOverridingImageTag(aesImageTag string, tag string) {
	i.ShowWrapped(fmt.Sprintf("   Overriding image tag from %q: %s.", aesImageTag, tag))
}

func (i *Installer) ShowOverridingHelmRepo(aesHelmRepo string, repo string) {
	i.ShowWrapped(fmt.Sprintf("   Overriding Helm repo from %q: %s.", aesHelmRepo, repo))
}

func (i *Installer) ShowAESCRDsButNoAESInstallation() {
	i.show.Println("-> Found Ambassador CRDs in your cluster, but no AES installation.")
}

func (i *Installer) ShowDownloadingImages() {
	i.show.Println("-> Downloading latest version")
}

func (i *Installer) ShowInstalling(version string) {
	i.show.Println(fmt.Sprintf("-> Installing Ambassador Edge Stack %s", version))
}

func (i *Installer) ShowCheckingAESPodDeployment() {
	i.show.Println("-> Checking the AES pod deployment")
}

func (i *Installer) ShowLocalClusterDetected() {
	i.show.Println("-> Local cluster detected. Not configuring automatic TLS.")
}

func (i *Installer) ShowProvisioningLoadBalancer() {
	i.show.Println("-> Provisioning a cloud load balancer")
}

func (i *Installer) ShowAESInstallAddress(address string) {
	i.show.Println("-> Your AES installation's address is", color.Bold.Sprintf(address))
}

func (i *Installer) ShowAESRespondingToACME() {
	i.show.Println("-> Checking that AES is responding to ACME challenge")
}

func (i *Installer) ShowWaiting(what string) {
	i.show.Printf("   Still waiting for %s. (This may take a minute.)", what)
}

func (i *Installer) ShowTimedOut(what string) {
	i.show.Printf("   Timed out waiting for %s (or interrupted)", what)
}

func (i *Installer) ShowAESConfiguringTLS() {
	i.show.Println("-> Automatically configuring TLS")
}

func (i *Installer) ShowFailedToCreateDNSName(dnsName string) {
	i.show.Println("-> Failed to create a DNS name:", dnsName)
}

func (i *Installer) ShowAcquiringDNSName(hostname string) {
	i.show.Println("-> Acquiring DNS name", color.Bold.Sprintf(hostname))
}

func (i *Installer) ShowObtainingTLSCertificate() {
	i.show.Println("-> Obtaining a TLS certificate from Let's Encrypt")
}

func (i *Installer) ShowTLSConfiguredSuccessfully() {
	i.show.Println("-> TLS configured successfully")
}

// AES installation partially complete -- other instructions follow.
func (i *Installer) ShowAESInstallationPartiallyComplete() {
	i.show.Println()
	i.show.Println("AES Installation Complete!")
	i.show.Println("========================================================================")
}

// AES installation complete!
func (i *Installer) ShowAESInstallationComplete() {
	i.show.Println()
	i.show.Println("AES Installation Complete!")
	i.show.Println("========================================================================")

	// Show congratulations message
	i.show.Println()
	i.ShowTemplated(color.Bold.Sprintf("Congratulations! You've successfully installed the Ambassador Edge Stack in your Kubernetes cluster. You can find it at your custom URL: https://{{.hostname}}/"))
	i.show.Println()
}

// Show how to use edgectl login in the future
func (i *Installer) ShowFutureLogin(hostname string) {
	i.show.Println()
	futureLogin := "In the future, to log in to the Ambassador Edge Policy Console, run\n%s"
	i.ShowWrapped(fmt.Sprintf(futureLogin, color.Bold.Sprintf("$ edgectl login " + hostname)))
}

