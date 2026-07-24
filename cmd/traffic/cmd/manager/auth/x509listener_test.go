package auth_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
)

// startX509Listener starts a listener on an OS-assigned loopback port and returns its
// dial address and a func that shuts it down and waits for Serve to return.
func startX509Listener(t *testing.T, pool *auth.ClientCAPool, tokens *auth.MintedTokens) (addr string, stop func()) {
	t.Helper()

	// Mirror NewService's wiring: OnChange only fires on a later reload, so the CA
	// pool's initial generation must be recorded explicitly.
	_, generation := pool.Snapshot()
	tokens.InvalidateAll(generation)
	pool.OnChange(tokens.InvalidateAll)

	ln, err := auth.NewX509Listener(0, pool, tokens)
	require.NoError(t, err)

	_, port, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = ln.Serve(ctx)
		close(done)
	}()
	return net.JoinHostPort("127.0.0.1", port), func() {
		cancel()
		<-done
	}
}

// dialX509 performs the client side of the auth handshake and decodes the single JSON
// response object.
func dialX509(t *testing.T, addr string, cert tls.Certificate) map[string]string {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, &tls.Config{
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: true, //nolint:gosec // the server cert is ephemeral and unauthenticated by design
	})
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))

	var resp map[string]string
	require.NoError(t, json.NewDecoder(conn).Decode(&resp))
	return resp
}

func TestX509Listener_AcceptsValidCertificate(t *testing.T) {
	ca := newTestCA(t)
	pool := auth.NewClientCAPool(context.Background(), fake.NewClientset(clientCAConfigMap(string(ca.pem))))
	tokens := auth.NewMintedTokens()

	addr, stop := startX509Listener(t, pool, tokens)
	defer stop()

	clientCert := ca.signClientCert(t, clientCertOpts{commonName: "test-user", organization: []string{"group-a"}})
	resp := dialX509(t, addr, clientCert)

	require.NotEmpty(t, resp["token"])
	require.NotEmpty(t, resp["expirationTimestamp"])
	assert.Empty(t, resp["error"])
	_, err := time.Parse(time.RFC3339, resp["expirationTimestamp"])
	assert.NoError(t, err)

	a := auth.NewAuthenticator(fake.NewClientset(), auth.WithMintedTokens(tokens))
	p, err := a.Authenticate(context.Background(), resp["token"])
	require.NoError(t, err)
	assert.Equal(t, "test-user", p.Username)
	assert.Equal(t, []string{"group-a", "system:authenticated"}, p.Groups)
	assert.NotEmpty(t, p.Extra["authentication.kubernetes.io/credential-id"])
}

// TestX509Listener_DistinctUIDsProduceDistinctPrincipals verifies parity with the API
// server's x509 authenticator: two certificates sharing a CommonName but carrying
// different x509 UID attributes must authenticate as distinct principals, so they don't
// collapse into the same session owner.
func TestX509Listener_DistinctUIDsProduceDistinctPrincipals(t *testing.T) {
	ca := newTestCA(t)
	pool := auth.NewClientCAPool(context.Background(), fake.NewClientset(clientCAConfigMap(string(ca.pem))))
	tokens := auth.NewMintedTokens()

	addr, stop := startX509Listener(t, pool, tokens)
	defer stop()

	a := auth.NewAuthenticator(fake.NewClientset(), auth.WithMintedTokens(tokens))

	cert1 := ca.signClientCert(t, clientCertOpts{commonName: "shared-cn", uid: "uid-1"})
	resp1 := dialX509(t, addr, cert1)
	require.NotEmpty(t, resp1["token"])
	p1, err := a.Authenticate(context.Background(), resp1["token"])
	require.NoError(t, err)

	cert2 := ca.signClientCert(t, clientCertOpts{commonName: "shared-cn", uid: "uid-2"})
	resp2 := dialX509(t, addr, cert2)
	require.NotEmpty(t, resp2["token"])
	p2, err := a.Authenticate(context.Background(), resp2["token"])
	require.NoError(t, err)

	assert.Equal(t, "uid-1", p1.UID)
	assert.Equal(t, "uid-2", p2.UID)
	assert.False(t, p1.SameAs(p2), "certificates with the same CN but different UIDs must not be treated as the same principal")
}

func TestX509Listener_RejectsCertificateFromUnknownCA(t *testing.T) {
	ca := newTestCA(t)
	other := newTestCA(t)
	pool := auth.NewClientCAPool(context.Background(), fake.NewClientset(clientCAConfigMap(string(ca.pem))))
	tokens := auth.NewMintedTokens()

	addr, stop := startX509Listener(t, pool, tokens)
	defer stop()

	clientCert := other.signClientCert(t, clientCertOpts{commonName: "intruder"})
	resp := dialX509(t, addr, clientCert)

	assert.NotEmpty(t, resp["error"])
	assert.Empty(t, resp["token"])
}

func TestX509Listener_RejectsExpiredCertificate(t *testing.T) {
	ca := newTestCA(t)
	pool := auth.NewClientCAPool(context.Background(), fake.NewClientset(clientCAConfigMap(string(ca.pem))))
	tokens := auth.NewMintedTokens()

	addr, stop := startX509Listener(t, pool, tokens)
	defer stop()

	clientCert := ca.signClientCert(t, clientCertOpts{
		commonName: "test-user",
		notBefore:  time.Now().Add(-2 * time.Hour),
		notAfter:   time.Now().Add(-time.Hour),
	})
	resp := dialX509(t, addr, clientCert)

	assert.NotEmpty(t, resp["error"])
	assert.Empty(t, resp["token"])
}

// TestX509Listener_AnonymousCommonNameGetsNoAuthenticatedGroup verifies parity with the
// API server's AuthenticatedGroupAdder: a certificate whose CommonName is
// system:anonymous must not gain the system:authenticated group.
func TestX509Listener_AnonymousCommonNameGetsNoAuthenticatedGroup(t *testing.T) {
	ca := newTestCA(t)
	pool := auth.NewClientCAPool(context.Background(), fake.NewClientset(clientCAConfigMap(string(ca.pem))))
	tokens := auth.NewMintedTokens()

	addr, stop := startX509Listener(t, pool, tokens)
	defer stop()

	clientCert := ca.signClientCert(t, clientCertOpts{commonName: "system:anonymous"})
	resp := dialX509(t, addr, clientCert)
	require.NotEmpty(t, resp["token"])

	a := auth.NewAuthenticator(fake.NewClientset(), auth.WithMintedTokens(tokens))
	p, err := a.Authenticate(context.Background(), resp["token"])
	require.NoError(t, err)
	assert.Equal(t, "system:anonymous", p.Username)
	assert.NotContains(t, p.Groups, "system:authenticated")
}

func TestX509Listener_RejectsEmptyCommonName(t *testing.T) {
	ca := newTestCA(t)
	pool := auth.NewClientCAPool(context.Background(), fake.NewClientset(clientCAConfigMap(string(ca.pem))))
	tokens := auth.NewMintedTokens()

	addr, stop := startX509Listener(t, pool, tokens)
	defer stop()

	clientCert := ca.signClientCert(t, clientCertOpts{commonName: ""})
	resp := dialX509(t, addr, clientCert)

	assert.NotEmpty(t, resp["error"])
	assert.Empty(t, resp["token"])
}
