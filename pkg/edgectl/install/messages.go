package edgectl

import (
	"fmt"

	"github.com/gookit/color"
)

// First message when beginning the AES Installation process
func (i *Installer) ShowFirstInstalling() {
	i.show.Println()
	i.show.Println(color.Bold.Sprintf("Installing the Ambassador Edge Stack"))
}

func (i *Installer) ShowFirstUpgrading() {
	i.show.Println()
	i.show.Println(color.Bold.Sprintf("Upgrading the Ambassador API Gateway to Ambassador Edge Stack"))
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

func (i *Installer) ShowBeginAOSSUpgrade() {
	i.show.Println()
	i.show.Println("========================================================================")
	i.show.Println("Beginning Ambassador API Gateway upgrade")
}

func (i *Installer) ShowFailedWhenLookingForExistingVersion() {
	i.show.Println("-> Failed when looking for an existing installation")
}

func (i *Installer) ShowAESVersionBeingInstalled() {
	i.show.Println(fmt.Sprintf("-> Installing the Ambassador Edge Stack %s.", i.version))
}

func (i *Installer) ShowAESExistingVersion(versionName string, method string) {
	i.show.Println(fmt.Sprintf("-> Ambassador Edge Stack %s was already installed using %s", versionName, method))
}

func (i *Installer) ShowAOSSExistingVersion(versionName string, method string) {
	i.show.Println(fmt.Sprintf("-> Ambassador API Gateway %s was already installed using %s", versionName, method))
}

func (i *Installer) FindingRepositoriesAndVersions() {
	i.show.Println("-> Finding repositories and chart versions")
}

func (i *Installer) ShowOverridingChartVersion(aesChartVersion string, version string) {
	i.show.Println(fmt.Sprintf("   Overriding chart version rule using %s = %s", aesChartVersion, version))
}

func (i *Installer) ShowOverridingImageRepo(aesImageRepo string, repo string) {
	i.show.Println(fmt.Sprintf("   Overriding image repo using %s = %s", aesImageRepo, repo))
}

func (i *Installer) ShowOverridingImageTag(aesImageTag string, tag string) {
	i.show.Println(fmt.Sprintf("   Overriding image tag using %s = %s", aesImageTag, tag))
}

func (i *Installer) ShowOverridingInstallOption(optionTag string, tagValue string) {
	i.show.Println(fmt.Sprintf("   Overriding install option using %s = %s", optionTag, tagValue))
}

func (i *Installer) ShowOverridingHelmRepo(aesHelmRepo string, repo string) {
	i.show.Println(fmt.Sprintf("   Overriding Helm repo using %s = %s", aesHelmRepo, repo))
}

func (i *Installer) ShowAESCRDsButNoAESInstallation() {
	i.show.Println("-> Found Ambassador CRDs in your cluster, but no Edge Stack installation.")
}

func (i *Installer) ShowDownloadingImages() {
	i.show.Println("-> Downloading latest version")
}

func (i *Installer) ShowInstalling(version string) {
	i.show.Println(fmt.Sprintf("-> Installing Ambassador Edge Stack %s", version))
}

func (i *Installer) ShowUpgrading(version string) {
	i.show.Println(fmt.Sprintf("-> Upgrading Ambassador API Gateway to Ambassador Edge Stack %s", version))
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
	i.show.Println("-> Your Ambassador Edge Stack installation's address is", color.Bold.Sprintf(address))
}

func (i *Installer) ShowAESRespondingToACME() {
	i.show.Println("-> Checking that Ambassador Edge Stack is responding to ACME challenge")
}

func (i *Installer) ShowWaiting(what string) {
	i.show.Printf("   Still waiting for %s. (This may take a minute.)", what)
}

func (i *Installer) ShowTimedOut(what string) {
	i.show.Printf("   Timed out waiting for %s (or interrupted)", what)
}

func (i *Installer) ShowLookingForExistingHost() {
	i.show.Printf("-> Looking for a Host resource in the existing installation")
}

func (i *Installer) ShowExistingHostFound(name, namespace string) {
	i.show.Printf("   Found %s in the %s namespace", name, namespace)
}

func (i *Installer) ShowAESConfiguringTLS() {
	i.show.Println("-> Automatically configuring TLS")
}

func (i *Installer) ShowFailedToCreateDNSName(message string) {
	i.show.Println("   Failed to create a DNS name:", message)
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
	i.show.Println("Ambassador Edge Stack Installation Complete!")
	i.show.Println("========================================================================")
}

// AES installation complete, but no DNS.
func (i *Installer) ShowAESInstallationCompleteNoDNS() {
	i.show.Println()
	i.show.Println("Ambassador Edge Stack Installation Complete!")
	i.show.Println("========================================================================")

	// Show congratulations message
	i.show.Println()
	message := "<bold>Congratulations! You've successfully installed the Ambassador Edge Stack in your Kubernetes cluster. However, we cannot connect to your cluster from the Internet, so we could not configure TLS automatically. "
	message += "If the IP address is reachable from your computer, you can access your installation without a DNS name.</>\n"
	i.ShowTemplated(message)
	i.show.Println()
}

// AES installation complete!
func (i *Installer) ShowAESInstallationComplete() {
	i.show.Println()
	i.show.Println("Ambassador Edge Stack Installation Complete!")
	i.show.Println("========================================================================")

	// Show congratulations message
	i.show.Println()
	message := color.Bold.Sprintf("Congratulations! You've successfully installed the Ambassador Edge Stack in your Kubernetes cluster. You can find it at your custom URL: https://{{.hostname}}/")
	i.ShowTemplated(message)
	i.show.Println()
}

// AES already installed
func (i *Installer) ShowAESAlreadyInstalled() {
	i.show.Println()
	i.show.Println("Ambassador Edge Stack Already Installed")
	i.show.Println("========================================================================")

	// Show congratulations message
	i.show.Println()
	message := color.Bold.Sprintf("Congratulations! The Ambassador Edge Stack is already installed in your Kubernetes cluster. You can find it at your custom URL: https://{{.hostname}}/")
	i.ShowTemplated(message)
}
