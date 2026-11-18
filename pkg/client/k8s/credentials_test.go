package k8s

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/transport/spdy"
	"k8s.io/client-go/transport/websocket"

	"github.com/telepresenceio/clog/testutil"
)

func tokenCredential(token string, expiry time.Time) []byte {
	if expiry.IsZero() {
		return fmt.Appendf(nil, `{"kind":"ExecCredential","status":{"token":%q}}`, token)
	}
	return fmt.Appendf(nil, `{"kind":"ExecCredential","status":{"token":%q,"expirationTimestamp":%q}}`,
		token, expiry.UTC().Format(time.RFC3339))
}

// countingFetcher returns a fetcher that answers with the successive results of next, and a call counter.
func countingFetcher(next func(n int) ([]byte, error)) (CredentialFetcher, *atomic.Int32) {
	var calls atomic.Int32
	return func(context.Context) ([]byte, error) {
		return next(int(calls.Add(1)))
	}, &calls
}

func TestFetchTokenSource_CachesUntilExpiryMargin(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	fetch, calls := countingFetcher(func(n int) ([]byte, error) {
		return tokenCredential(fmt.Sprintf("tok%d", n), time.Now().Add(execTokenSafetyMargin+time.Hour)), nil
	})
	src := newFetchTokenSource(fetch)
	for range 3 {
		tok, err := src.Token(ctx)
		require.NoError(t, err)
		require.Equal(t, "tok1", tok)
	}
	require.Equal(t, int32(1), calls.Load())
}

func TestFetchTokenSource_RefreshesBeforeExpiry(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	// The token expires in 30 seconds, which is inside the one minute safety margin.
	fetch, calls := countingFetcher(func(n int) ([]byte, error) {
		return tokenCredential(fmt.Sprintf("tok%d", n), time.Now().Add(30*time.Second)), nil
	})
	src := newFetchTokenSource(fetch)
	tok, err := src.Token(ctx)
	require.NoError(t, err)
	require.Equal(t, "tok1", tok)
	tok, err = src.Token(ctx)
	require.NoError(t, err)
	require.Equal(t, "tok2", tok)
	require.Equal(t, int32(2), calls.Load())
}

func TestFetchTokenSource_NoExpiryUsesDefaultTTL(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	fetch, calls := countingFetcher(func(n int) ([]byte, error) {
		return tokenCredential("tok", time.Time{}), nil
	})
	src := newFetchTokenSource(fetch)
	before := time.Now()
	_, err := src.Token(ctx)
	require.NoError(t, err)
	_, err = src.Token(ctx)
	require.NoError(t, err)
	require.Equal(t, int32(1), calls.Load())
	require.WithinDuration(t, before.Add(execTokenDefaultTTL), src.expiry, time.Second)
}

func TestFetchTokenSource_FetchErrorIsReturnedAndNotCached(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	boom := errors.New("boom")
	fetch, calls := countingFetcher(func(n int) ([]byte, error) {
		if n == 1 {
			return nil, boom
		}
		return tokenCredential("tok", time.Time{}), nil
	})
	src := newFetchTokenSource(fetch)
	_, err := src.Token(ctx)
	require.ErrorIs(t, err, boom)
	tok, err := src.Token(ctx)
	require.NoError(t, err)
	require.Equal(t, "tok", tok)
	require.Equal(t, int32(2), calls.Load())
}

func TestFetchTokenSource_ResetTokenOlderThan(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	fetch, _ := countingFetcher(func(n int) ([]byte, error) {
		return tokenCredential(fmt.Sprintf("tok%d", n), time.Time{}), nil
	})
	src := newFetchTokenSource(fetch)
	_, err := src.Token(ctx)
	require.NoError(t, err)

	// A reset that predates the credential keeps it.
	src.ResetTokenOlderThan(time.Now().Add(-time.Hour))
	tok, err := src.Token(ctx)
	require.NoError(t, err)
	require.Equal(t, "tok1", tok)

	// A reset after the credential was fetched drops it.
	src.ResetTokenOlderThan(time.Now().Add(time.Second))
	tok, err = src.Token(ctx)
	require.NoError(t, err)
	require.Equal(t, "tok2", tok)
}

func TestApplyCredentials_Certificate(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	expiry := time.Now().Add(time.Hour).Truncate(time.Second)
	fetch, calls := countingFetcher(func(int) ([]byte, error) {
		return fmt.Appendf(nil, `{"status":{"clientCertificateData":"CERT","clientKeyData":"KEY","expirationTimestamp":%q}}`,
			expiry.UTC().Format(time.RFC3339)), nil
	})
	rc := &rest.Config{}
	src, gotExpiry, err := applyCredentials(ctx, rc, fetch)
	require.NoError(t, err)
	require.Nil(t, src)
	require.Equal(t, []byte("CERT"), rc.CertData)
	require.Equal(t, []byte("KEY"), rc.KeyData)
	require.True(t, expiry.Equal(gotExpiry))
	require.Nil(t, rc.WrapTransport)
	require.Equal(t, int32(1), calls.Load())
}

func TestApplyCredentials_Errors(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	_, _, err := applyCredentials(ctx, &rest.Config{}, func(context.Context) ([]byte, error) {
		return nil, errors.New("plugin failed")
	})
	require.ErrorContains(t, err, "plugin failed")

	_, _, err = applyCredentials(ctx, &rest.Config{}, func(context.Context) ([]byte, error) {
		return []byte(`{"status":{}}`), nil
	})
	require.ErrorContains(t, err, "neither a token nor a client certificate")
}

// authRecorder is a server that records the Authorization header of its requests and answers with
// the next status in statuses (200 when there are none).
type authRecorder struct {
	mu       sync.Mutex
	headers  []string
	statuses []int
}

func (a *authRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	a.headers = append(a.headers, r.Header.Get("Authorization"))
	code := http.StatusOK
	if len(a.statuses) > 0 {
		code, a.statuses = a.statuses[0], a.statuses[1:]
	}
	a.mu.Unlock()
	w.WriteHeader(code)
}

func (a *authRecorder) seen() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.headers...)
}

func TestApplyCredentials_TokenIsSentAndRefreshedAfter401(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	rec := &authRecorder{statuses: []int{http.StatusUnauthorized}}
	srv := httptest.NewServer(rec)
	defer srv.Close()

	fetch, calls := countingFetcher(func(n int) ([]byte, error) {
		return tokenCredential(fmt.Sprintf("tok%d", n), time.Time{}), nil
	})
	rc := &rest.Config{Host: srv.URL}
	src, _, err := applyCredentials(ctx, rc, fetch)
	require.NoError(t, err)
	require.NotNil(t, src)

	// The first request must start on a later clock reading than the fetch.
	for t0 := time.Now(); !time.Now().After(t0); {
		runtime.Gosched()
	}

	hc, err := rest.HTTPClientFor(rc)
	require.NoError(t, err)
	for range 3 {
		resp, err := hc.Get(srv.URL)
		require.NoError(t, err)
		resp.Body.Close()
	}
	// The 401 on the first request drops the credential, so the second request fetches a new one.
	require.Equal(t, []string{"Bearer tok1", "Bearer tok2", "Bearer tok2"}, rec.seen())
	require.Equal(t, int32(2), calls.Load())
}

// The port-forward dialers use the wrappers of the rest.Config on their upgrade requests. The recorder
// answers without upgrading, so the round trip itself may fail; what counts is the header it carried.
func TestApplyCredentials_TokenIsSentOnPortForwardUpgrade(t *testing.T) {
	ctx := testutil.NewContext(t, false)

	newConfig := func(srvURL string) *rest.Config {
		fetch, _ := countingFetcher(func(int) ([]byte, error) {
			return tokenCredential("pf-token", time.Time{}), nil
		})
		rc := &rest.Config{Host: srvURL}
		_, _, err := applyCredentials(ctx, rc, fetch)
		require.NoError(t, err)
		return rc
	}

	t.Run("spdy", func(t *testing.T) {
		rec := &authRecorder{}
		srv := httptest.NewServer(rec)
		defer srv.Close()
		rt, _, err := spdy.RoundTripperFor(newConfig(srv.URL))
		require.NoError(t, err)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/portforward", nil)
		require.NoError(t, err)
		resp, err := rt.RoundTrip(req)
		if err == nil {
			resp.Body.Close()
		}
		require.Equal(t, []string{"Bearer pf-token"}, rec.seen())
	})

	t.Run("websocket", func(t *testing.T) {
		rec := &authRecorder{}
		srv := httptest.NewServer(rec)
		defer srv.Close()
		rt, _, err := websocket.RoundTripperFor(newConfig(srv.URL))
		require.NoError(t, err)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/portforward", nil)
		require.NoError(t, err)
		resp, err := rt.RoundTrip(req)
		if err == nil {
			resp.Body.Close()
		}
		require.Equal(t, []string{"Bearer pf-token"}, rec.seen())
	})
}

const staticKubeconfig = `
apiVersion: v1
kind: Config
current-context: ctx
contexts:
- name: ctx
  context: {cluster: cl, user: usr}
clusters:
- name: cl
  cluster: {server: "https://127.0.0.1:1"}
users:
- name: usr
  user: {}
`

func TestNewRootKubeconfig_TokenFeedsClientAndManager(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	fetch, calls := countingFetcher(func(n int) ([]byte, error) {
		return tokenCredential(fmt.Sprintf("tok%d", n), time.Time{}), nil
	})
	kc, err := NewRootKubeconfig(ctx, nil, "", []byte(staticKubeconfig), fetch)
	require.NoError(t, err)
	require.NotNil(t, kc.RestConfig.WrapTransport)
	require.Nil(t, kc.RestConfig.ExecProvider)
	require.True(t, kc.CredentialExpiry().IsZero())

	// The manager token comes from the same source, so the first credential is fetched once.
	src := newManagerTokenSource(kc)
	require.NotNil(t, src)
	tok, err := src.Token(ctx)
	require.NoError(t, err)
	require.Equal(t, "tok1", tok)
	require.Equal(t, int32(1), calls.Load())

	bearer, _ := ClientAuthMethods(kc)
	require.True(t, bearer)

	// The cluster client carries the same token.
	rec := &authRecorder{}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	rc := *kc.RestConfig
	rc.Host = srv.URL
	hc, err := rest.HTTPClientFor(&rc)
	require.NoError(t, err)
	resp, err := hc.Get(srv.URL)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, []string{"Bearer tok1"}, rec.seen())
	require.Equal(t, int32(1), calls.Load())
}

// selfSignedPEM returns a certificate and key in PEM format.
func selfSignedPEM(t *testing.T) (certPEM, keyPEM string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "tester"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
}

func TestNewRootKubeconfig_CertificateCredential(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	expiry := time.Now().Add(time.Hour).Truncate(time.Second)
	certPEM, keyPEM := selfSignedPEM(t)
	fetch, _ := countingFetcher(func(int) ([]byte, error) {
		return json.Marshal(map[string]any{"status": map[string]any{
			"clientCertificateData": certPEM,
			"clientKeyData":         keyPEM,
			"expirationTimestamp":   expiry.UTC().Format(time.RFC3339),
		}})
	})
	kc, err := NewRootKubeconfig(ctx, nil, "", []byte(staticKubeconfig), fetch)
	require.NoError(t, err)
	require.Equal(t, []byte(certPEM), kc.RestConfig.CertData)
	require.Equal(t, []byte(keyPEM), kc.RestConfig.KeyData)
	require.True(t, expiry.Equal(kc.CredentialExpiry()))
	// A certificate is not a bearer token.
	require.Nil(t, newManagerTokenSource(kc))
}

func TestNewRootKubeconfig_StaticCredentialsAreUntouched(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	data := staticKubeconfig[:len(staticKubeconfig)-len("  user: {}\n")] + "  user: {token: static-token}\n"
	kc, err := NewRootKubeconfig(ctx, nil, "", []byte(data), nil)
	require.NoError(t, err)
	require.Equal(t, "static-token", kc.RestConfig.BearerToken)
	require.Nil(t, kc.RestConfig.WrapTransport)
	require.True(t, kc.CredentialExpiry().IsZero())
}

func TestNewRootKubeconfig_RefusesCredentialPlugin(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	data := staticKubeconfig[:len(staticKubeconfig)-len("  user: {}\n")] + `  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1
      interactiveMode: Never
      command: "/bin/false"
`
	_, err := NewRootKubeconfig(ctx, nil, "", []byte(data), nil)
	require.ErrorContains(t, err, "credential plugin")
	_, err = NewRootKubeconfig(ctx, nil, "", []byte(data), func(context.Context) ([]byte, error) { return nil, errors.New("must not be called") })
	require.ErrorContains(t, err, "credential plugin")
}
