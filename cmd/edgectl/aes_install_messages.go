package main


// First message when beginning the AES Installation process
func BeginAESInstallMessage()  {
}

// Unable to get a kubectl path.
func NoKubectlError(err error) Result {
	return Result{
		Err: err,
	}
}

// Unable to get cluster information
func NoClusterError(err error) Result {
	return Result{
		Err: err,
	}
}

// Unable to get client configuration or namespace
func GetRestConfigError(err error) Result {
	return Result{
		Err: err,
	}
}


// Unable to create a new CoreV1Client for the given configuration.
func NewForConfigError(err error) Result {
	return Result{
		Err: err,
	}
}


// Unable to get versions via kubectl
func CaptureKubectlError(err error) Result {
	return Result{
		Err: err,
	}
}


// Unable to fetch the AES CRD manifests (aes-crds.yaml)
func AESCRDManifestsError(err error) Result {
	return Result{
		Err: err,
	}
}


// Unable to fetch the AES manifests (aes.yaml)
func AESManifestsError(err error) Result {
	return Result{
		Err: err,
	}
}


// Unable to parse the downloaded AES manifests
func ManifestParsingError(err error) Result {
	return Result{
		Err: err,
	}
}


// Existing AES CRD's of incompatible version
func IncompatibleCRDVersionsError(err error) Result {
	return Result{
		Err: err,
	}
}


// Existing AES CRD's, unable to upgrade.
func ExistingCRDsError(err error) Result {
	return Result{
		Err: err,
	}
}


// Unable to kubectl apply the aes-crd.yaml manifests
func InstallCRDsError(err error) Result {
	return Result{
		Err: err,
	}
}


// 90-second timeout on waiting for aes-crd.yaml manifests to be established
func WaitCRDsError(err error) Result {
	return Result{
		Err: err,
	}
}


// Unable to kubectl apply the aes.yaml manifests
func InstallAESError(err error) Result {
	return Result{
		Err: err,
	}
}


//90-second timeout on waiting for aes.yaml manifests to be deployed and available
func WaitForAESError(err error) Result {
	return Result{
		Err: err,
	}
}


// Unable to get the AES Install ID via kubectl exec to ask for the pod ID
func AESPodStartupError(err error) Result {
	return Result{
		Err: err,
	}
}


// docker-desktop, minikube, or kind: local cluster so no automatic TLS.
func KnownLocalClusterResult() Result {
	return Result{
		Err: err,
	}
}


// Unable to provision a load balancer (failed to retrieve the IP address)
func LoadBalancerError(err error) Result {
	return Result{
		Err: err,
	}
}


// AES failed to respond to the ACME challenge.  This may be because AES did not start quickly enough or
// if the AES load balancer is not reachable.
func AESACMEChallengeError(err error) Result {
	return Result{
		Err: err,
	}
}

// Unable to make an HTTP Post to Metriton at https://metriton.datawire.io/register-domain
// and so cannot acquire a DNS name for the cluster's load balancer.
func DNSNamePostFailure(err error) Result {
	return Result{
		Err: err,
	}
}

// Unable to fetch the response from the HTTP Post to Metriton.
func DNSNameBodyFailure(err error) Result {
	return Result{
		Err: err,
	}
}

// Successful installation but no DNS.
func AESInstalledNoDNSResult() Result {
	return Result{
		Err: nil,
	}
}

// The DNS name propagation timed out, so unable to resolve the name.
func DNSPropagationError(err error) Result {
	return Result{
		Err: err,
	}
}

// In attempting to kubectl apply the hostResource yaml, kubectl failed.
func HostResourceCreationError(err error) Result {
	return Result{
		Err: err,
	}
}

// Unable to acquire a TLS certificate from Let's Encrypt
func CertificateProvisionError(err error) Result {
	return Result{
		Err: err,
	}
}

// Unable to acquire a TLS certificate from Let's Encrypt
func HostRetrievalError(err error) Result {
	return Result{
		Err: err,
	}
}

// AES installation complete!
func AESInstallCompleteMessage()  {
}


// Attempted to log in to the cluster but failed.
func AESLoginError(err error) Result {
	return Result{
		Err: err,
	}
}

// AES login successful!
func AESLoginSuccessResult()  Result {
}







