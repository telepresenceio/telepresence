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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// generateCAPEM returns a throwaway self-signed CA certificate in PEM form, distinct
// for each commonName, for exercising ClientCAPool's content-change detection without
// depending on the auth_test package's cert helpers.
func generateCAPEM(t *testing.T, commonName string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func configMapWithData(pemData string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: clientCAConfigMapName, Namespace: clientCAConfigMapNamespace},
		Data:       map[string]string{clientCAConfigMapKey: pemData},
	}
}

// TestClientCAPool_OnChange_FiresOnContentChange verifies that reload invokes the
// registered callback only when the bundle's content actually changed, not on the
// initial load and not when a reload re-reads identical content.
func TestClientCAPool_OnChange_FiresOnContentChange(t *testing.T) {
	ci := fake.NewClientset(configMapWithData(generateCAPEM(t, "ca-1")))
	p := NewClientCAPool(context.Background(), ci)
	require.NotNil(t, p.Pool())
	_, initialGen := p.Snapshot()
	assert.Equal(t, uint64(1), initialGen, "the first successful load must be generation 1")

	var fired int
	var lastGen uint64
	p.OnChange(func(generation uint64) { fired++; lastGen = generation })

	// Reloading identical content must not fire the callback.
	p.reload(context.Background())
	assert.Equal(t, 0, fired)

	// Changing the bundle's content must fire the callback exactly once per change,
	// with the new generation.
	_, err := ci.CoreV1().ConfigMaps(clientCAConfigMapNamespace).Update(
		context.Background(), configMapWithData(generateCAPEM(t, "ca-2")), metav1.UpdateOptions{})
	require.NoError(t, err)
	p.reload(context.Background())
	assert.Equal(t, 1, fired)
	assert.Equal(t, initialGen+1, lastGen)

	// Reloading that same new content again must not fire the callback again.
	p.reload(context.Background())
	assert.Equal(t, 1, fired)

	_, gen := p.Snapshot()
	assert.Equal(t, initialGen+1, gen)
}

// TestClientCAPool_OnChange_FiresOnFirstLoad verifies that reload invokes a callback
// registered before any load has ever succeeded for that first successful load too, with
// generation 1 -- not just for later content changes. Without this, a store wired up via
// OnChange before the first load would sit at generation 0 forever, mismatching every
// generation Snapshot subsequently reports and refusing every Mint.
func TestClientCAPool_OnChange_FiresOnFirstLoad(t *testing.T) {
	ci := fake.NewClientset(configMapWithData(generateCAPEM(t, "ca-1")))
	p := &ClientCAPool{client: ci}
	var fired int
	var lastGen uint64
	p.OnChange(func(generation uint64) { fired++; lastGen = generation })
	p.reload(context.Background())
	require.NotNil(t, p.Pool())
	assert.Equal(t, 1, fired, "the first successful load must fire the callback")
	assert.Equal(t, uint64(1), lastGen)
}

// TestClientCAPool_OnChange_RunsBeforeSnapshotPublished verifies that, for as long as
// onChange is running, Snapshot still returns the pool and generation from before the
// reload -- the new snapshot is only stored once onChange has returned. This is what
// makes the CA-swap/mint race fail closed: a verify+Mint pair that runs entirely inside
// this window still sees, and mints against, the old generation consistently; one that
// straddles it finds MintedTokens already moved to the new generation while Snapshot
// still reports the old one, and Mint refuses it.
func TestClientCAPool_OnChange_RunsBeforeSnapshotPublished(t *testing.T) {
	ci := fake.NewClientset(configMapWithData(generateCAPEM(t, "ca-1")))
	p := NewClientCAPool(context.Background(), ci)
	oldPool, oldGen := p.Snapshot()
	require.NotNil(t, oldPool)

	var sawPool *x509.CertPool
	var sawGen uint64
	p.OnChange(func(uint64) { sawPool, sawGen = p.Snapshot() })

	_, err := ci.CoreV1().ConfigMaps(clientCAConfigMapNamespace).Update(
		context.Background(), configMapWithData(generateCAPEM(t, "ca-2")), metav1.UpdateOptions{})
	require.NoError(t, err)
	p.reload(context.Background())

	assert.Same(t, oldPool, sawPool, "Snapshot from inside onChange must still return the pre-reload pool")
	assert.Equal(t, oldGen, sawGen, "Snapshot from inside onChange must still return the pre-reload generation")

	newPool, newGen := p.Snapshot()
	assert.NotSame(t, oldPool, newPool)
	assert.Equal(t, oldGen+1, newGen)
}

// TestClientCAPool_Reload_BoundedByAPITimeout verifies that reload returns within
// clientCAReloadTimeout when the ConfigMap GET is stalled, so that a stalled API server
// cannot pin mu -- and therefore a handshake slot blocked on ReloadOnFailure --
// indefinitely. reload now performs the GET directly against a context bounded by
// clientCAReloadTimeout, relying on client-go honoring context cancellation the way it
// does in production; the fake reactor here plays that role itself, blocking until its
// own bounded context is done and then returning the same error a cancelled real request
// would.
func TestClientCAPool_Reload_BoundedByAPITimeout(t *testing.T) {
	ci := fake.NewClientset()
	reactorCtx, cancel := context.WithTimeout(context.Background(), clientCAReloadTimeout)
	defer cancel()
	ci.PrependReactor("get", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		<-reactorCtx.Done()
		return true, nil, reactorCtx.Err()
	})

	p := &ClientCAPool{client: ci}

	done := make(chan struct{})
	start := time.Now()
	go func() {
		p.reload(context.Background())
		close(done)
	}()

	select {
	case <-done:
		assert.Less(t, time.Since(start), clientCAReloadTimeout+5*time.Second)
	case <-time.After(clientCAReloadTimeout + 5*time.Second):
		t.Fatal("reload did not return within the bound")
	}
}
