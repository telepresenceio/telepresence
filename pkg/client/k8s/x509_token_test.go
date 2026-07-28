package k8s

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json/v2"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
)

// generateX509TestCert returns a bare-bones self-signed leaf certificate and
// its PEM-encoded cert and key, suitable for use as either the server's or
// the client's TLS certificate in these tests.
func generateX509TestCert(t *testing.T) (tls.Certificate, []byte, []byte) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := x509.Certificate{SerialNumber: big.NewInt(1)}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, priv.Public(), priv)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(priv)
	require.NoError(t, err)
	certBuf := &bytes.Buffer{}
	require.NoError(t, pem.Encode(certBuf, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}))
	keyBuf := &bytes.Buffer{}
	require.NoError(t, pem.Encode(keyBuf, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	certPEM, keyPEM := certBuf.Bytes(), keyBuf.Bytes()
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)
	return cert, certPEM, keyPEM
}

// startX509AuthTestServer starts a TLS listener that requires (but does not
// verify) a client certificate, and for every accepted connection writes the
// bytes returned by respond and closes the connection. It returns the
// listener address and a counter of completed handshakes.
func startX509AuthTestServer(t *testing.T, respond func(handshakeNum int) []byte) (string, *int32) {
	t.Helper()
	serverCert, _, _ := generateX509TestCert(t)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAnyClientCert,
		MinVersion:   tls.VersionTLS13,
	})
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })

	var count int32
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				tlsConn, ok := conn.(*tls.Conn)
				if !ok {
					return
				}
				if err := tlsConn.Handshake(); err != nil {
					return
				}
				n := int(atomic.AddInt32(&count, 1))
				_, _ = tlsConn.Write(respond(n))
			}()
		}
	}()
	return ln.Addr().String(), &count
}

// dialerTo returns an x509AuthDialer that ignores the requested address and
// always connects to addr, mirroring the port-forward dialer's signature
// without needing a real pod or port-forward session.
func dialerTo(addr string) x509AuthDialer {
	return func(ctx context.Context, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	}
}

func testClientTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	clientCert, _, _ := generateX509TestCert(t)
	return &tls.Config{
		Certificates:       []tls.Certificate{clientCert},
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS13,
	}
}

func TestX509TokenSource_Deactivated(t *testing.T) {
	src := &x509TokenSource{tlsConfig: testClientTLSConfig(t)}
	token, err := src.Token(context.Background())
	require.NoError(t, err)
	require.Empty(t, token)
}

func TestX509TokenSource_TokenRetrievalAndCaching(t *testing.T) {
	addr, count := startX509AuthTestServer(t, func(n int) []byte {
		resp, _ := json.Marshal(x509AuthResponse{
			Token:               fmt.Sprintf("tok-%d", n),
			ExpirationTimestamp: time.Now().Add(time.Hour).Format(time.RFC3339),
		})
		return resp
	})

	src := &x509TokenSource{tlsConfig: testClientTLSConfig(t)}
	src.activate(dialerTo(addr), "pod/traffic-manager-abc.ambassador:15007#uid")

	token, err := src.Token(context.Background())
	require.NoError(t, err)
	require.Equal(t, "tok-1", token)
	require.EqualValues(t, 1, atomic.LoadInt32(count))

	// A second call within the cached expiry must not re-handshake.
	token, err = src.Token(context.Background())
	require.NoError(t, err)
	require.Equal(t, "tok-1", token)
	require.EqualValues(t, 1, atomic.LoadInt32(count))
}

func TestX509TokenSource_ReHandshakeOnExpiry(t *testing.T) {
	addr, count := startX509AuthTestServer(t, func(n int) []byte {
		resp, _ := json.Marshal(x509AuthResponse{
			Token: fmt.Sprintf("tok-%d", n),
			// Within the 1-minute safety margin: every response is
			// already considered expired as soon as it is cached.
			ExpirationTimestamp: time.Now().Add(5 * time.Second).Format(time.RFC3339),
		})
		return resp
	})

	src := &x509TokenSource{tlsConfig: testClientTLSConfig(t)}
	src.activate(dialerTo(addr), "pod/traffic-manager-abc.ambassador:15007#uid")

	token1, err := src.Token(context.Background())
	require.NoError(t, err)
	require.Equal(t, "tok-1", token1)

	token2, err := src.Token(context.Background())
	require.NoError(t, err)
	require.Equal(t, "tok-2", token2)
	require.EqualValues(t, 2, atomic.LoadInt32(count))
}

func TestX509TokenSource_ErrorResponse(t *testing.T) {
	addr, _ := startX509AuthTestServer(t, func(int) []byte {
		resp, _ := json.Marshal(x509AuthResponse{Error: "certificate not trusted"})
		return resp
	})

	src := &x509TokenSource{tlsConfig: testClientTLSConfig(t)}
	src.activate(dialerTo(addr), "pod/traffic-manager-abc.ambassador:15007#uid")

	token, err := src.Token(context.Background())
	require.Error(t, err)
	require.Empty(t, token)
	require.ErrorContains(t, err, "certificate not trusted")
}

func TestX509TokenSource_HandshakeTimesOutWithoutHang(t *testing.T) {
	serverCert, _, _ := generateX509TestCert(t)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAnyClientCert,
		MinVersion:   tls.VersionTLS13,
	})
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })

	// The server completes the handshake but never writes a response;
	// stop unblocks its connection-handling goroutine once the test is
	// done observing the client-side timeout.
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				if tlsConn, ok := conn.(*tls.Conn); ok {
					_ = tlsConn.Handshake()
				}
				<-stop
			}()
		}
	}()

	src := &x509TokenSource{tlsConfig: testClientTLSConfig(t)}
	src.activate(dialerTo(ln.Addr().String()), "pod/traffic-manager-abc.ambassador:15007#uid")

	// A caller-supplied deadline shorter than x509ExchangeTimeout must
	// still be honored: context.WithTimeout picks the earlier of the two.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	var token string
	var tokErr error
	go func() {
		token, tokErr = src.Token(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Token did not return within the exchange timeout")
	}
	require.Error(t, tokErr)
	require.Empty(t, token)
}

func TestX509TokenSource_OversizedResponseRejected(t *testing.T) {
	addr, _ := startX509AuthTestServer(t, func(int) []byte {
		return bytes.Repeat([]byte("x"), x509ResponseSizeLimit+1)
	})

	src := &x509TokenSource{tlsConfig: testClientTLSConfig(t)}
	src.activate(dialerTo(addr), "pod/traffic-manager-abc.ambassador:15007#uid")

	token, err := src.Token(context.Background())
	require.Error(t, err)
	require.Empty(t, token)
	require.ErrorContains(t, err, "exceeds")
}

func TestX509TokenSource_TrailingDataRejected(t *testing.T) {
	addr, _ := startX509AuthTestServer(t, func(int) []byte {
		resp, _ := json.Marshal(x509AuthResponse{Token: "tok"})
		return append(resp, []byte("garbage")...)
	})

	src := &x509TokenSource{tlsConfig: testClientTLSConfig(t)}
	src.activate(dialerTo(addr), "pod/traffic-manager-abc.ambassador:15007#uid")

	token, err := src.Token(context.Background())
	require.Error(t, err)
	require.Empty(t, token)
}

func TestX509TokenSource_ExpiryClampedToMaxTTL(t *testing.T) {
	addr, _ := startX509AuthTestServer(t, func(int) []byte {
		resp, _ := json.Marshal(x509AuthResponse{
			Token:               "tok",
			ExpirationTimestamp: time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		})
		return resp
	})

	src := &x509TokenSource{tlsConfig: testClientTLSConfig(t)}
	src.activate(dialerTo(addr), "pod/traffic-manager-abc.ambassador:15007#uid")

	before := time.Now()
	token, err := src.Token(context.Background())
	require.NoError(t, err)
	require.Equal(t, "tok", token)

	src.mu.Lock()
	expiry := src.expiry
	src.mu.Unlock()
	require.WithinDuration(t, before.Add(x509TokenSourceMaxTTL), expiry, time.Second)
}

func TestX509TokenSource_ActivateClearsCachedToken(t *testing.T) {
	addr, count := startX509AuthTestServer(t, func(n int) []byte {
		resp, _ := json.Marshal(x509AuthResponse{
			Token:               fmt.Sprintf("tok-%d", n),
			ExpirationTimestamp: time.Now().Add(time.Hour).Format(time.RFC3339),
		})
		return resp
	})

	src := &x509TokenSource{tlsConfig: testClientTLSConfig(t)}
	src.activate(dialerTo(addr), "pod/traffic-manager-abc.ambassador:15007#uid")

	token1, err := src.Token(context.Background())
	require.NoError(t, err)
	require.Equal(t, "tok-1", token1)

	// Re-activation, even against the same target, discards the cached
	// token: a reconnect may land on a manager instance whose in-memory
	// token store does not recognize it.
	src.activate(dialerTo(addr), "pod/traffic-manager-abc.ambassador:15007#uid")

	token2, err := src.Token(context.Background())
	require.NoError(t, err)
	require.Equal(t, "tok-2", token2)
	require.EqualValues(t, 2, atomic.LoadInt32(count))
}

func TestX509TokenSource_ConcurrentTokenCallsShareOneHandshake(t *testing.T) {
	release := make(chan struct{})
	addr, count := startX509AuthTestServer(t, func(n int) []byte {
		<-release
		resp, _ := json.Marshal(x509AuthResponse{
			Token:               fmt.Sprintf("tok-%d", n),
			ExpirationTimestamp: time.Now().Add(time.Hour).Format(time.RFC3339),
		})
		return resp
	})

	src := &x509TokenSource{tlsConfig: testClientTLSConfig(t)}
	src.activate(dialerTo(addr), "pod/traffic-manager-abc.ambassador:15007#uid")

	const callers = 5
	tokens := make([]string, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := range callers {
		go func(i int) {
			defer wg.Done()
			tokens[i], errs[i] = src.Token(context.Background())
		}(i)
	}

	// Give the callers time to pile up behind the in-flight exchange
	// before letting the single handshake complete.
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()

	for i := range callers {
		require.NoError(t, errs[i])
		require.Equal(t, "tok-1", tokens[i])
	}
	require.EqualValues(t, 1, atomic.LoadInt32(count))
}

func TestX509TokenSource_BlockedCallerReturnsOnContextExpiry(t *testing.T) {
	release := make(chan struct{})
	addr, _ := startX509AuthTestServer(t, func(n int) []byte {
		<-release
		resp, _ := json.Marshal(x509AuthResponse{
			Token:               fmt.Sprintf("tok-%d", n),
			ExpirationTimestamp: time.Now().Add(time.Hour).Format(time.RFC3339),
		})
		return resp
	})

	src := &x509TokenSource{tlsConfig: testClientTLSConfig(t)}
	src.activate(dialerTo(addr), "pod/traffic-manager-abc.ambassador:15007#uid")

	// Occupy the in-flight exchange guard with a first call that will not
	// complete until release is closed below.
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_, _ = src.Token(context.Background())
	}()

	// Give the first call time to acquire the guard and start its
	// handshake before a second caller arrives behind it.
	time.Sleep(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	var tokErr error
	go func() {
		defer close(done)
		_, tokErr = src.Token(ctx)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Token did not return within the caller's own deadline")
	}
	require.ErrorIs(t, tokErr, context.DeadlineExceeded)

	// Unblock the first exchange and let it finish so the server's
	// connection-handling goroutine exits cleanly.
	close(release)
	select {
	case <-firstDone:
	case <-time.After(5 * time.Second):
		t.Fatal("first Token call never completed")
	}
}

func TestNewX509TokenSource(t *testing.T) {
	t.Run("no client certificate", func(t *testing.T) {
		kc := &Kubeconfig{RestConfig: &rest.Config{BearerToken: "some-token"}}
		require.Nil(t, newX509TokenSource(kc))
	})

	t.Run("nil kubeconfig or rest config", func(t *testing.T) {
		require.Nil(t, newX509TokenSource(nil))
		require.Nil(t, newX509TokenSource(&Kubeconfig{}))
	})

	t.Run("static client certificate", func(t *testing.T) {
		_, certPEM, keyPEM := generateX509TestCert(t)
		kc := &Kubeconfig{RestConfig: &rest.Config{
			TLSClientConfig: rest.TLSClientConfig{
				CertData: certPEM,
				KeyData:  keyPEM,
			},
		}}
		src := newX509TokenSource(kc)
		require.NotNil(t, src)
		require.True(t, src.tlsConfig.InsecureSkipVerify)
		require.Equal(t, uint16(tls.VersionTLS13), src.tlsConfig.MinVersion)
		require.NotNil(t, src.tlsConfig.GetClientCertificate)
	})
}

func TestManagerAuthTokenSource(t *testing.T) {
	staticSrc := func(token string, err error) managerTokenSource {
		return staticTokenSourceFunc(func(context.Context) (string, error) { return token, err })
	}

	t.Run("bearer only", func(t *testing.T) {
		c := &managerAuthTokenSource{bearer: staticSrc("tok", nil)}
		tok, err := c.Token(context.Background())
		require.NoError(t, err)
		require.Equal(t, "tok", tok)
	})

	t.Run("bearer errNoBearerToken falls back to x509", func(t *testing.T) {
		c := &managerAuthTokenSource{
			bearer: staticSrc("", errNoBearerToken),
			x509:   staticSrc("x509tok", nil),
		}
		tok, err := c.Token(context.Background())
		require.NoError(t, err)
		require.Equal(t, "x509tok", tok)
	})

	t.Run("bearer empty falls back to x509", func(t *testing.T) {
		c := &managerAuthTokenSource{
			bearer: staticSrc("", nil),
			x509:   staticSrc("x509tok", nil),
		}
		tok, err := c.Token(context.Background())
		require.NoError(t, err)
		require.Equal(t, "x509tok", tok)
	})

	t.Run("genuine bearer error is not masked", func(t *testing.T) {
		wantErr := errors.New("boom")
		c := &managerAuthTokenSource{
			bearer: staticSrc("", wantErr),
			x509:   staticSrc("x509tok", nil),
		}
		tok, err := c.Token(context.Background())
		require.ErrorIs(t, err, wantErr)
		require.Empty(t, tok)
	})

	t.Run("no sources", func(t *testing.T) {
		c := &managerAuthTokenSource{}
		tok, err := c.Token(context.Background())
		require.NoError(t, err)
		require.Empty(t, tok)
	})
}

type staticTokenSourceFunc func(context.Context) (string, error)

func (f staticTokenSourceFunc) Token(ctx context.Context) (string, error) {
	return f(ctx)
}
