package k8s

import (
	"context"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/transport"

	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

// applyCredentials fetches the first credential and installs it in rc. A token is
// installed as a refreshing token source, a client certificate in the TLS config.
// The returned source is nil for a certificate-only credential. The expiry is
// the time when the certificate expires, or zero.
func applyCredentials(ctx context.Context, rc *rest.Config, fetch CredentialFetcher) (*execTokenSource, time.Time, error) {
	src := newFetchTokenSource(fetch)
	st, err := src.credential(ctx)
	if err != nil {
		return nil, time.Time{}, errcat.User.Newf("unable to get cluster credentials: %v", err)
	}
	if st.Token == "" && st.ClientCertificateData == "" {
		return nil, time.Time{}, errcat.User.New("unable to get cluster credentials: the credential has neither a token nor a client certificate")
	}
	var expiry time.Time
	if st.ClientCertificateData != "" {
		rc.CertData = []byte(st.ClientCertificateData)
		rc.KeyData = []byte(st.ClientKeyData)
		if st.ExpirationTimestamp != nil {
			expiry = st.ExpirationTimestamp.Time
		}
	}
	if st.Token == "" {
		return nil, expiry, nil
	}
	rc.Wrap(transport.ResettableTokenSourceWrapTransport(restTokenSource{ctx: ctx, src: src}))
	return src, expiry, nil
}

// CredentialExpiry returns the time when a client certificate that came from a
// CredentialFetcher expires, or the zero time when there is no such certificate
// or it has no expiry.
func (kf *Kubeconfig) CredentialExpiry() time.Time {
	if kf == nil {
		return time.Time{}
	}
	return kf.credentialExpiry
}
