package k8s

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
)

func TestClientAuthMethods(t *testing.T) {
	t.Run("cert-only kubeconfig", func(t *testing.T) {
		_, certPEM, keyPEM := generateX509TestCert(t)
		kc := &Kubeconfig{RestConfig: &rest.Config{
			TLSClientConfig: rest.TLSClientConfig{
				CertData: certPEM,
				KeyData:  keyPEM,
			},
		}}
		bearer, x509 := ClientAuthMethods(kc)
		require.False(t, bearer)
		require.True(t, x509)
	})

	t.Run("token kubeconfig", func(t *testing.T) {
		kc := &Kubeconfig{RestConfig: &rest.Config{BearerToken: "some-token"}}
		bearer, x509 := ClientAuthMethods(kc)
		require.True(t, bearer)
		require.False(t, x509)
	})

	t.Run("nil kubeconfig", func(t *testing.T) {
		bearer, x509 := ClientAuthMethods(nil)
		require.False(t, bearer)
		require.False(t, x509)
	})
}
