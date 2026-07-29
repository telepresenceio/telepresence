package k8s

// ClientAuthMethods reports which credential kinds kc's kubeconfig can
// produce for authenticating to the traffic-manager: bearer is true when it
// yields a bearer token, x509 is true when it carries client-certificate
// credentials. The two are not mutually exclusive.
func ClientAuthMethods(kc *Kubeconfig) (bearer, x509 bool) {
	return newManagerTokenSource(kc) != nil, newX509TokenSource(kc) != nil
}
