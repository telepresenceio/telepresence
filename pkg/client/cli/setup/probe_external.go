package setup

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"sort"
	"time"

	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// certManagerGroupVersion is the API group/version cert-manager registers
// its Certificate CRD under.
const certManagerGroupVersion = "cert-manager.io/v1"

// certManagerResource is the plural resource name of cert-manager's
// Certificate CRD.
const certManagerResource = "certificates"

// probeExternal records whether cert-manager is installed and which
// kubernetes.io/tls Secrets exist in the manager namespace.
func (p *Prober) probeExternal(ctx context.Context) ExternalFacts {
	facts := ExternalFacts{CertManager: p.certManagerFinding()}

	secrets, err := p.KubeClient.CoreV1().Secrets(p.ManagerNamespace).List(ctx, meta.ListOptions{
		FieldSelector: "type=" + string(core.SecretTypeTLS),
	})
	switch {
	case apierrors.IsForbidden(err):
		facts.SecretsListDenied = true
		return facts
	case err != nil:
		facts.SecretsListError = err.Error()
		return facts
	}

	now := time.Now()
	for i := range secrets.Items {
		s := &secrets.Items[i]
		if s.Type != core.SecretTypeTLS {
			continue
		}
		facts.TLSSecrets = append(facts.TLSSecrets, tlsSecretFacts(s, now))
	}
	sortTLSSecrets(facts.TLSSecrets)
	return facts
}

// certManagerFinding reports whether cert-manager's Certificate CRD is
// served: a discovery lookup, unknown on any error other than the group
// simply not being served.
func (p *Prober) certManagerFinding() Finding {
	list, err := p.KubeClient.Discovery().ServerResourcesForGroupVersion(certManagerGroupVersion)
	switch {
	case apierrors.IsNotFound(err):
		return Finding{Verdict: VerdictNo, Evidence: []string{"the " + certManagerGroupVersion + " API group is not served"}}
	case err != nil:
		return Finding{Verdict: VerdictUnknown, Evidence: []string{fmt.Sprintf("cert-manager discovery failed: %v", err)}}
	}
	for _, r := range list.APIResources {
		if r.Name == certManagerResource {
			return Finding{Verdict: VerdictYes, Evidence: []string{"the " + certManagerGroupVersion + " " + certManagerResource + " resource is served"}}
		}
	}
	return Finding{Verdict: VerdictNo, Evidence: []string{
		certManagerGroupVersion + " is served but its " + certManagerResource + " resource was not found",
	}}
}

// tlsSecretFacts reads a kubernetes.io/tls Secret's name and, best effort,
// its leaf certificate's DNS names and expiry. A Secret with no tls.crt data
// (a metadata-only List, or an RBAC-restricted field) yields the name alone.
func tlsSecretFacts(s *core.Secret, now time.Time) TLSSecretFacts {
	f := TLSSecretFacts{Name: s.Name}
	leaf := s.Data["tls.crt"]
	if len(leaf) == 0 {
		return f
	}
	cert, err := parseLeafCertificate(leaf)
	if err != nil {
		f.ReadError = err.Error()
		return f
	}
	f.DNSNames = cert.DNSNames
	f.NotAfter = cert.NotAfter.Format(time.RFC3339)
	f.Expired = now.After(cert.NotAfter)
	f.ExpiringSoon = !f.Expired && now.Add(certExpiryWarning).After(cert.NotAfter)
	return f
}

// parseLeafCertificate decodes a PEM-encoded leaf certificate, as stored in
// a kubernetes.io/tls Secret's tls.crt.
func parseLeafCertificate(pemBytes []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("tls.crt contains no PEM certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}

// sortTLSSecrets orders list by name, except that certManagerSecretName --
// the chart's own cert-manager target -- always sorts first.
func sortTLSSecrets(list []TLSSecretFacts) {
	sort.Slice(list, func(i, j int) bool {
		if list[i].Name == certManagerSecretName {
			return true
		}
		if list[j].Name == certManagerSecretName {
			return false
		}
		return list[i].Name < list[j].Name
	})
}
