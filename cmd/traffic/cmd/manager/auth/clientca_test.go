package auth_test

import (
	"context"
	"crypto/x509"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
)

func TestClientCAPool_LoadsValidPEM(t *testing.T) {
	ca := newTestCA(t)
	ci := fake.NewClientset(clientCAConfigMap(string(ca.pem)))

	pool := auth.NewClientCAPool(context.Background(), ci)
	require.NotNil(t, pool.Pool())

	leaf := ca.signClientCert(t, clientCertOpts{commonName: "u"})
	leafCert, err := x509.ParseCertificate(leaf.Certificate[0])
	require.NoError(t, err)
	_, err = leafCert.Verify(x509.VerifyOptions{
		Roots:     pool.Pool(),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	assert.NoError(t, err)
}

func TestClientCAPool_MissingConfigMap(t *testing.T) {
	ci := fake.NewClientset()
	pool := auth.NewClientCAPool(context.Background(), ci)
	assert.Nil(t, pool.Pool())
}

func TestClientCAPool_MissingKey(t *testing.T) {
	ci := fake.NewClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "extension-apiserver-authentication", Namespace: "kube-system"},
		Data:       map[string]string{"other-key": "x"},
	})
	pool := auth.NewClientCAPool(context.Background(), ci)
	assert.Nil(t, pool.Pool())
}

func TestClientCAPool_InvalidPEM(t *testing.T) {
	ci := fake.NewClientset(clientCAConfigMap("not a pem"))
	pool := auth.NewClientCAPool(context.Background(), ci)
	assert.Nil(t, pool.Pool())
}

func TestClientCAPool_ReloadOnFailure_RateLimited(t *testing.T) {
	ci := fake.NewClientset()
	var gets atomic.Int32
	ci.PrependReactor("get", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		gets.Add(1)
		return false, nil, nil
	})

	pool := auth.NewClientCAPool(context.Background(), ci)
	assert.Nil(t, pool.Pool())
	afterInit := gets.Load()
	assert.Equal(t, int32(1), afterInit)

	pool.ReloadOnFailure(context.Background())
	pool.ReloadOnFailure(context.Background())
	assert.Equal(t, afterInit+1, gets.Load(), "the second call within the cooldown must not trigger another read")
}

// TestClientCAPool_RecoversFromFailedInitialLoad reproduces the P1 recovery bug: if the
// ConfigMap is unavailable when the pool is constructed, the initial load fails and the
// pool sits at generation 0. A token store wired up via OnChange and initialized at that
// same generation 0 must still be able to mint once the ConfigMap becomes available and a
// reload succeeds -- which requires the first successful load to fire onChange, not just
// a later content change, since otherwise the store stays at generation 0 forever while
// Snapshot moves on to generation 1, and every Mint refuses with
// ErrCAGenerationMismatch with no way to recover.
func TestClientCAPool_RecoversFromFailedInitialLoad(t *testing.T) {
	ci := fake.NewClientset() // no ConfigMap: the initial load in NewClientCAPool fails.
	pool := auth.NewClientCAPool(context.Background(), ci)
	require.Nil(t, pool.Pool())
	_, gen := pool.Snapshot()
	require.Equal(t, uint64(0), gen)

	tokens := auth.NewMintedTokens()
	pool.OnChange(tokens.InvalidateAll)
	tokens.InvalidateAll(gen) // wiring at startup: initialize the store at generation 0.

	// The ConfigMap becomes available and a reload succeeds.
	ca := newTestCA(t)
	_, err := ci.CoreV1().ConfigMaps("kube-system").Create(
		context.Background(), clientCAConfigMap(string(ca.pem)), metav1.CreateOptions{})
	require.NoError(t, err)
	pool.ReloadOnFailure(context.Background())

	newPool, newGen := pool.Snapshot()
	require.NotNil(t, newPool)
	require.Equal(t, uint64(1), newGen)

	// Minting with the new snapshot's generation must succeed.
	p := &auth.Principal{Username: "u"}
	token, _, err := tokens.Mint(p, "fp-1", time.Now().Add(time.Hour), newGen)
	require.NoError(t, err)
	assert.NotEmpty(t, token)
}

// TestClientCAPool_MintedTokens_GenerationGuardsCAPoolSwap is a deterministic
// reproduction of the CA-change/mint race: a handshake that verified a certificate
// against an old CA snapshot must not be able to mint a token once the pool has been
// swapped and every existing token invalidated, even though the certificate was valid
// when verification happened -- but the same certificate can mint again once
// re-verified against the new snapshot. It also proves there is no window during the
// swap where Snapshot and MintedTokens can disagree about the generation: onChange runs
// -- and invalidates every token -- strictly before the new snapshot is published, so a
// Snapshot call made from inside onChange still sees the pre-swap pool and generation,
// and a Mint against that generation is refused for the same reason it would be refused
// after the swap completes.
func TestClientCAPool_MintedTokens_GenerationGuardsCAPoolSwap(t *testing.T) {
	ca := newTestCA(t)
	ci := fake.NewClientset(clientCAConfigMap(string(ca.pem)))
	pool := auth.NewClientCAPool(context.Background(), ci)
	tokens := auth.NewMintedTokens()

	// The handshake verifies against generation 1, the pool's initial generation.
	_, gen := pool.Snapshot()
	require.Equal(t, uint64(1), gen)
	tokens.InvalidateAll(gen)

	p := &auth.Principal{Username: "u"}

	// Capture what Snapshot and a Mint against it see from inside onChange, i.e. after
	// MintedTokens has already moved to the new generation but before the new
	// pool/generation snapshot is published.
	var midGen uint64
	var midMintErr error
	pool.OnChange(func(newGeneration uint64) {
		tokens.InvalidateAll(newGeneration)
		_, midGen = pool.Snapshot()
		_, _, midMintErr = tokens.Mint(p, "fp-1", time.Now().Add(time.Hour), midGen)
	})

	// The reload rotates the CA and, via OnChange, invalidates every minted token
	// before the paused handshake resumes.
	newCA := newTestCA(t)
	_, err := ci.CoreV1().ConfigMaps("kube-system").Update(
		context.Background(), clientCAConfigMap(string(newCA.pem)), metav1.UpdateOptions{})
	require.NoError(t, err)
	pool.ReloadOnFailure(context.Background())

	// Mid-reload, Snapshot still reported the pre-swap generation, and Mint against it
	// was refused since MintedTokens had already moved on by the time onChange ran.
	assert.Equal(t, gen, midGen)
	assert.ErrorIs(t, midMintErr, auth.ErrCAGenerationMismatch)

	_, newGen := pool.Snapshot()
	require.Equal(t, gen+1, newGen)

	// Minting against the generation the handshake actually verified against is
	// refused, since the pool has since moved on.
	_, _, err = tokens.Mint(p, "fp-1", time.Now().Add(time.Hour), gen)
	assert.ErrorIs(t, err, auth.ErrCAGenerationMismatch)

	// Re-verifying against the now-current snapshot and retrying Mint succeeds.
	token, _, err := tokens.Mint(p, "fp-1", time.Now().Add(time.Hour), newGen)
	require.NoError(t, err)
	assert.NotEmpty(t, token)
}
