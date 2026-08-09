package k8s

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"net/url"
	"os"
	"strings"

	"google.golang.org/grpc/credentials"
	"k8s.io/client-go/transport"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

// usesExternalTransport reports whether cc selects the external endpoint
// (cc.ManagerAddress) instead of the classic port-forward transport.
func usesExternalTransport(cc *client.Cluster) bool {
	return cc.ManagerAddress != ""
}

// externalManagerScheme is the only cluster.managerAddress scheme currently
// supported.
const externalManagerScheme = "tls"

// parseManagerAddress validates addr and splits it into the host:port to
// dial and the TLS ServerName to verify.
func parseManagerAddress(addr string) (hostPort, serverName string, err error) {
	u, err := url.Parse(addr)
	if err != nil {
		return "", "", errcat.User.Newf("cluster.managerAddress %q: %v", addr, err)
	}
	if u.Scheme != externalManagerScheme {
		return "", "", errcat.User.Newf(
			"cluster.managerAddress %q: unsupported scheme %q, only %q is currently supported",
			addr, u.Scheme, externalManagerScheme)
	}
	if u.Hostname() == "" || u.Port() == "" {
		return "", "", errcat.User.Newf("cluster.managerAddress %q: must include both a host and a port", addr)
	}
	return u.Host, u.Hostname(), nil
}

// managerServerCredentials builds TLS credentials trusting caSpec's CA when
// set, else system roots. getClientCert, when non-nil, presents the client
// certificate directly since no bearer token is available.
func managerServerCredentials(
	serverName, caSpec string,
	getClientCert func(*tls.CertificateRequestInfo) (*tls.Certificate, error),
) (credentials.TransportCredentials, error) {
	tlsConfig := &tls.Config{
		ServerName:           serverName,
		MinVersion:           tls.VersionTLS12,
		GetClientCertificate: getClientCert,
	}
	if caSpec != "" {
		caPEM, err := resolveManagerServerCA(caSpec)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, errcat.User.New("cluster.managerServerCA does not contain a valid PEM certificate")
		}
		tlsConfig.RootCAs = pool
	}
	return credentials.NewTLS(tlsConfig), nil
}

// externalClientCertificate returns a GetClientCertificate callback from
// kc's client-certificate credentials, or nil when it has none, for direct
// presentation in the external TLS handshake.
func externalClientCertificate(kc *Kubeconfig) func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	if kc == nil || kc.RestConfig == nil {
		return nil
	}
	tc, err := kc.RestConfig.TransportConfig()
	if err != nil || !(tc.HasCertAuth() || tc.HasCertCallback()) {
		return nil
	}
	tlsConfig, err := transport.TLSConfigFor(tc)
	if err != nil || tlsConfig == nil {
		return nil
	}
	if tlsConfig.GetClientCertificate != nil {
		return tlsConfig.GetClientCertificate
	}
	if len(tlsConfig.Certificates) > 0 {
		cert := tlsConfig.Certificates[0]
		return func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return &cert, nil
		}
	}
	return nil
}

// resolveManagerServerCA interprets the cluster.managerServerCA config value,
// accepted in three forms: a literal PEM block, the path to a file
// containing one, or a base64-encoded PEM block.
func resolveManagerServerCA(spec string) ([]byte, error) {
	if strings.HasPrefix(spec, "-----BEGIN") {
		return []byte(spec), nil
	}
	if _, err := os.Stat(spec); err == nil {
		data, err := os.ReadFile(spec)
		if err != nil {
			return nil, errcat.User.Newf("unable to read cluster.managerServerCA file %q: %v", spec, err)
		}
		return data, nil
	}
	data, err := base64.StdEncoding.DecodeString(spec)
	if err != nil {
		return nil, errcat.User.New(
			"cluster.managerServerCA is neither a PEM block, an existing file path, nor valid base64-encoded PEM")
	}
	return data, nil
}
