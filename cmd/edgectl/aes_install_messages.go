package main

import (
	"fmt"

	"github.com/gookit/color"
)

// First message when beginning the AES Installation process
func (i *Installer) ShowBeginAESInstallation() {
	i.show.Println("========================================================================")
	i.show.Println("Beginning Ambassador Edge Stack Installation")
	i.show.Println()
}

func (i *Installer) ShowAESVersionBeingInstalled() {
	i.show.Println(fmt.Sprintf("-> Installing the Ambassador Edge Stack %s.", i.version))
}

func (i *Installer) ShowAESExistingVersion(versionName string) {
	i.show.Println(fmt.Sprintf("   Ambassador Edge Stack %s already installed", versionName))
}

func (i *Installer) ShowAESCRDsButNoAESInstallation() {
	i.show.Println("-> Found Ambassador CRDs in your cluster, but no AES installation.")
}

func (i *Installer) ShowDownloadingImages() {
	i.show.Println("-> Downloading images. (This may take a minute.)")
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

func (i *Installer) ShowFailedToCreateDNSName(message string) {
	i.show.Println("-> Failed to create a DNS name:", message)
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

// AES installation complete!
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
	i.ShowTemplated(color.Bold.Sprintf("Congratulations! You've successfully installed the Ambassador Edge Stack in your Kubernetes cluster. Visit https://{{.hostname}}/"))
	i.show.Println()

}
