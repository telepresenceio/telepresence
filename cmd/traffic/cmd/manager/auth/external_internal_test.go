package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

// This file covers ExternalInterceptor (external.go): the admission controls that must
// run before a bearer token can trigger a TokenReview (concurrency cap, QPS/burst
// limiter, oversized-metadata rejection), and the transport-principal credential rules
// (both-presented rejection, stale CA generation, expired certificate).

func externalCtxWithBearer(token string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs(authorizationHeader, bearerPrefix+token))
}

func externalCtxWithTransportPrincipal(tp *TransportPrincipal) context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{AuthInfo: &externalAuthInfo{TransportPrincipal: tp}})
}

func TestExternalInterceptor_BothCredentialsRejected(t *testing.T) {
	ci := fake.NewClientset()
	k8sapi.InstallFakeTokenReviews(ci, func(string, []string) *authnv1.TokenReviewStatus {
		return &authnv1.TokenReviewStatus{Authenticated: true, User: authnv1.UserInfo{Username: "u", UID: "1"}}
	})
	ext := NewExternalInterceptor(NewInterceptor(NewAuthenticator(ci), ModeEnforcing), nil, nil)

	ctx := externalCtxWithBearer("good")
	ctx = peer.NewContext(ctx, &peer.Peer{AuthInfo: &externalAuthInfo{
		TransportPrincipal: &TransportPrincipal{Principal: &Principal{Username: "cert-user"}, NotAfter: time.Now().Add(time.Hour)},
	}})

	_, err := ext.authenticate(ctx, "/telepresence.manager.Manager/Connect")
	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
	assert.Contains(t, err.Error(), "present exactly one")
}

func TestExternalInterceptor_RateLimiter_ResourceExhausted(t *testing.T) {
	ci := fake.NewClientset()
	k8sapi.InstallFakeTokenReviews(ci, func(string, []string) *authnv1.TokenReviewStatus {
		return &authnv1.TokenReviewStatus{Authenticated: false}
	})
	ext := NewExternalInterceptor(NewInterceptor(NewAuthenticator(ci), ModeEnforcing), nil, nil)
	// A burst of exactly one, with no refill, isolates the limiter from real time.
	ext.limiter = rate.NewLimiter(0, 1)

	_, err1 := ext.authenticate(externalCtxWithBearer("bad-1"), "/m")
	require.Error(t, err1)
	assert.Equal(t, codes.Unauthenticated, status.Code(err1), "the first call consumes the only token and reaches the authenticator")

	_, err2 := ext.authenticate(externalCtxWithBearer("bad-2"), "/m")
	require.Error(t, err2)
	assert.Equal(t, codes.ResourceExhausted, status.Code(err2), "the second call must be rejected by the limiter before reaching the authenticator")
}

func TestExternalInterceptor_ConcurrencyCap_ResourceExhausted(t *testing.T) {
	ci := fake.NewClientset()
	k8sapi.InstallFakeTokenReviews(ci, func(string, []string) *authnv1.TokenReviewStatus {
		return &authnv1.TokenReviewStatus{Authenticated: false}
	})
	ext := NewExternalInterceptor(NewInterceptor(NewAuthenticator(ci), ModeEnforcing), nil, nil)
	ext.sem = make(chan struct{}, 1)
	ext.sem <- struct{}{} // occupy the only slot, as a concurrent authentication would.

	_, err := ext.authenticate(externalCtxWithBearer("any"), "/m")
	require.Error(t, err)
	assert.Equal(t, codes.ResourceExhausted, status.Code(err))
}

func TestExternalInterceptor_OversizedMetadata(t *testing.T) {
	ci := fake.NewClientset()
	var calls atomic.Int32
	k8sapi.InstallFakeTokenReviews(ci, func(string, []string) *authnv1.TokenReviewStatus {
		calls.Add(1)
		return &authnv1.TokenReviewStatus{Authenticated: false}
	})
	ext := NewExternalInterceptor(NewInterceptor(NewAuthenticator(ci), ModeEnforcing), nil, nil)

	huge := strings.Repeat("a", externalMaxAuthMetadataLen+1)
	_, err := ext.authenticate(externalCtxWithBearer(huge), "/m")
	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
	assert.Equal(t, int32(0), calls.Load(), "an oversized authorization value must never reach TokenReview")
}

func TestExternalInterceptor_TransportPrincipal_ExpiredCertificate(t *testing.T) {
	ext := NewExternalInterceptor(NewInterceptor(NewAuthenticator(fake.NewClientset()), ModeEnforcing), nil, nil)
	tp := &TransportPrincipal{
		Principal: &Principal{Username: "cert-user"},
		NotAfter:  time.Now().Add(-time.Minute),
	}
	_, err := ext.authenticate(externalCtxWithTransportPrincipal(tp), "/m")
	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
	assert.Contains(t, err.Error(), "expired")
}

func TestExternalInterceptor_TransportPrincipal_StaleCAGeneration(t *testing.T) {
	ci := fake.NewClientset(configMapWithData(generateCAPEM(t, "ca-1")))
	pool := NewClientCAPool(context.Background(), ci)
	_, gen1 := pool.Snapshot()

	_, err := ci.CoreV1().ConfigMaps(clientCAConfigMapNamespace).Update(
		context.Background(), configMapWithData(generateCAPEM(t, "ca-2")), metav1.UpdateOptions{})
	require.NoError(t, err)
	pool.reload(context.Background())
	_, gen2 := pool.Snapshot()
	require.NotEqual(t, gen1, gen2)

	ext := NewExternalInterceptor(NewInterceptor(NewAuthenticator(fake.NewClientset()), ModeEnforcing), pool, nil)
	tp := &TransportPrincipal{
		Principal:    &Principal{Username: "cert-user"},
		CAGeneration: gen1,
		NotAfter:     time.Now().Add(time.Hour),
	}
	_, err = ext.authenticate(externalCtxWithTransportPrincipal(tp), "/m")
	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
	assert.Contains(t, err.Error(), "superseded")
}

func TestExternalInterceptor_TransportPrincipal_CurrentGenerationSucceeds(t *testing.T) {
	ci := fake.NewClientset(configMapWithData(generateCAPEM(t, "ca-1")))
	pool := NewClientCAPool(context.Background(), ci)
	_, gen := pool.Snapshot()

	ext := NewExternalInterceptor(NewInterceptor(NewAuthenticator(fake.NewClientset()), ModeEnforcing), pool, nil)
	tp := &TransportPrincipal{
		Principal:    &Principal{Username: "cert-user", UID: "u1"},
		CAGeneration: gen,
		NotAfter:     time.Now().Add(time.Hour),
	}
	newCtx, err := ext.authenticate(externalCtxWithTransportPrincipal(tp), "/m")
	require.NoError(t, err)
	p := PrincipalFrom(newCtx)
	require.NotNil(t, p)
	assert.Equal(t, "cert-user", p.Username)
}

func TestExternalInterceptor_NoCredentialsRejected(t *testing.T) {
	ext := NewExternalInterceptor(NewInterceptor(NewAuthenticator(fake.NewClientset()), ModeEnforcing), nil, nil)
	_, err := ext.authenticate(context.Background(), "/m")
	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
}

// TestIdleConn_TimerFires_ClosesConnection verifies that an idleConn whose idle timer
// fires before MarkAuthenticated is called closes the underlying connection.
func TestIdleConn_TimerFires_ClosesConnection(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()

	fired := make(chan struct{})
	ic := &idleConn{Conn: server}
	ic.timer = time.AfterFunc(5*time.Millisecond, func() {
		_ = server.Close()
		close(fired)
	})

	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("idle timer never fired")
	}
	_, err := ic.Read(make([]byte, 1))
	assert.Error(t, err)
}

// TestIdleConn_MarkAuthenticated_PreventsClose verifies that calling MarkAuthenticated
// before the idle timer fires cancels it, leaving the connection open.
func TestIdleConn_MarkAuthenticated_PreventsClose(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	defer server.Close()

	fired := make(chan struct{})
	ic := &idleConn{Conn: server}
	ic.timer = time.AfterFunc(5*time.Millisecond, func() {
		_ = server.Close()
		close(fired)
	})
	ic.MarkAuthenticated()

	select {
	case <-fired:
		t.Fatal("idle timer must not fire after MarkAuthenticated")
	case <-time.After(50 * time.Millisecond):
	}
}

// TestCertCache_ReloadsOnMtimeChange verifies that certCache.get reloads the certificate
// when the file's mtime changes, and serves the cached value otherwise -- the mechanism
// that lets the external listener pick up a rotated Secret without a manager restart.
func TestCertCache_ReloadsOnMtimeChange(t *testing.T) {
	dir := t.TempDir()
	writeTestCert(t, dir, "first")

	cc := newCertCache(dir)
	got1, err := cc.get()
	require.NoError(t, err)

	// Re-fetching without touching the files must return the cached certificate.
	got2, err := cc.get()
	require.NoError(t, err)
	assert.Same(t, got1, got2)

	writeTestCert(t, dir, "second")
	future := time.Now().Add(time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(dir, "tls.crt"), future, future))

	got3, err := cc.get()
	require.NoError(t, err)
	assert.NotEqual(t, got1.Certificate[0], got3.Certificate[0], "a changed mtime must trigger a reload")
}

// writeTestCert writes a throwaway self-signed certificate and key, distinguished by
// commonName, as tls.crt/tls.key in dir.
func writeTestCert(t *testing.T, dir, commonName string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tls.crt"), certPEM, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tls.key"), keyPEM, 0o600))
}
