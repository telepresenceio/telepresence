package edgectl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/datawire/ambassador/pkg/metriton"
)

// ConfigureTLS configures TLS for a valid email address
func (i *Installer) ConfigureTLS(emailAddress string) (string, string, *http.Response, Result) {
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
		return "", "", nil, i.resDNSNamePostError(err)
	}

	content, err := ioutil.ReadAll(resp.Body)
	resp.Body.Close()

	if err != nil {
		return "", "", nil, i.resDNSNameBodyError(err)
	}

	// With and without DNS.  In case of no DNS, different error messages and result handling.
	dnsMessage := "" // Message for error reporting in case of no DNS
	hostName := ""   // Login to this (hostname or IP address)

	// Was there a DNS name post response?
	if resp.StatusCode == 200 {
		// Have DNS name--now wait for it to propagate.
		i.hostname = string(content)
		i.ShowAcquiringDNSName(i.hostname)

		// Wait for DNS to propagate. This tries to avoid waiting for a ten
		// minute error backoff if the ACME registration races ahead of the DNS
		// name appearing for LetsEncrypt.

		if err := i.loopUntil("DNS propagation to this host", i.CheckHostnameFound, lc2); err != nil {
			return "", "", nil, i.resDNSPropagationError(err)
		}

		i.Report("dns_name_propagated")

		// Create a Host resource
		hostResource := fmt.Sprintf(hostManifest, i.hostname, i.hostname, emailAddress)
		if err := i.kubectl.Apply(hostResource, ""); err != nil {
			return "", "", nil, i.resHostResourceCreationError(err)
		}

		i.ShowObtainingTLSCertificate()

		if err := i.loopUntil("TLS certificate acquisition", i.CheckACMEIsDone, lc5); err != nil {
			return "", "", nil, i.resCertificateProvisionError(err)
		}

		i.Report("cert_provisioned")
		i.ShowTLSConfiguredSuccessfully()

		if _, err := i.kubectl.Get("host", i.hostname, ""); err != nil {
			return "", "", nil, i.resHostRetrievalError(err)
		}

		// Made it through with DNS and TLS.  Set hostName to the DNS name that was given.
		hostName = i.hostname
	} else {
		// Failure case: couldn't create DNS name.  Set hostName the IP address of the host.
		hostName = i.address
		dnsMessage = strings.TrimSpace(string(content))
		i.ShowFailedToCreateDNSName(dnsMessage)
	}
	return dnsMessage, hostName, resp, Result{}
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
