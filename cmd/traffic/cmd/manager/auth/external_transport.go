package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"google.golang.org/grpc/credentials"

	x509req "k8s.io/apiserver/pkg/authentication/request/x509"

	"github.com/telepresenceio/clog"
)

// externalTLSHandshakeTimeout bounds the external listener's TLS handshake, mirroring
// x509HandshakeTimeout: the socket deadline is cleared once the handshake completes, so
// it never bounds the RPCs that follow.
const externalTLSHandshakeTimeout = 10 * time.Second

// externalMaxConcurrentHandshakes bounds the number of TLS handshakes in flight on the
// external listener, mirroring x509MaxConcurrentHandshakes.
const externalMaxConcurrentHandshakes = 64

// TransportPrincipal is the identity derived from a verified client certificate at the
// external listener's TLS handshake, carrying the CA generation and expiry that later
// per-RPC re-validation checks against.
type TransportPrincipal struct {
	Principal    *Principal
	CAGeneration uint64
	NotAfter     time.Time
}

// externalAuthInfo is the credentials.AuthInfo attached to an external-listener
// connection. TransportPrincipal is nil for a bearer-only caller.
type externalAuthInfo struct {
	credentials.TLSInfo
	TransportPrincipal *TransportPrincipal
	idle               *idleConn
}

// certCache loads a TLS certificate from a directory containing tls.crt/tls.key,
// re-reading only when the certificate file's mtime changes, so that a rotated Secret
// (a new certificate written by the kubelet's Secret volume sync) takes effect on the
// next handshake without a manager restart.
type certCache struct {
	dir string

	mu    sync.Mutex
	cert  *tls.Certificate
	mtime time.Time
}

func newCertCache(dir string) *certCache {
	return &certCache{dir: dir}
}

func (c *certCache) get() (*tls.Certificate, error) {
	certPath := filepath.Join(c.dir, "tls.crt")
	keyPath := filepath.Join(c.dir, "tls.key")
	fi, err := os.Stat(certPath)
	if err != nil {
		return nil, fmt.Errorf("external tls: stat %s: %w", certPath, err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cert != nil && fi.ModTime().Equal(c.mtime) {
		return c.cert, nil
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("external tls: load keypair from %s: %w", c.dir, err)
	}
	c.cert = &cert
	c.mtime = fi.ModTime()
	return c.cert, nil
}

// externalConnTracker tracks the still-open external connections that authenticated with
// a client certificate, so that a client-CA generation change can proactively close the
// ones verified against a now-superseded generation instead of waiting for the
// interceptor to reject their next RPC.
type externalConnTracker struct {
	mu    sync.Mutex
	conns map[net.Conn]uint64 // conn -> the CA generation it was verified against
}

// NewExternalConnTracker creates an empty connection tracker for the external listener,
// closed over by NewExternalTransportCredentials and passed to ClientCAPool.OnChange via
// its CloseStale method.
func NewExternalConnTracker() *externalConnTracker {
	return &externalConnTracker{conns: make(map[net.Conn]uint64)}
}

// track wraps conn so that it removes itself from the tracker when closed, and records
// it under generation.
func (t *externalConnTracker) track(conn net.Conn, generation uint64) net.Conn {
	tc := &trackedConn{Conn: conn, tracker: t}
	t.mu.Lock()
	t.conns[tc] = generation
	t.mu.Unlock()
	return tc
}

func (t *externalConnTracker) untrack(conn net.Conn) {
	t.mu.Lock()
	delete(t.conns, conn)
	t.mu.Unlock()
}

// CloseStale closes every tracked connection verified against a generation other than
// currentGeneration. Intended as the ClientCAPool.OnChange callback for the external
// listener's own CA pool.
func (t *externalConnTracker) CloseStale(currentGeneration uint64) {
	t.mu.Lock()
	var stale []net.Conn
	for c, gen := range t.conns {
		if gen != currentGeneration {
			stale = append(stale, c)
		}
	}
	t.mu.Unlock()
	for _, c := range stale {
		_ = c.Close()
	}
}

type trackedConn struct {
	net.Conn
	tracker *externalConnTracker
}

func (c *trackedConn) Close() error {
	c.tracker.untrack(c)
	return c.Conn.Close()
}

// externalTransportCreds terminates TLS for the external listener: a server
// certificate reloaded from certCache, an optional client certificate verified against
// caPool (bearer-only callers are still admitted), bounded handshake concurrency and
// duration, and a TransportPrincipal derived from a verified certificate.
type externalTransportCreds struct {
	credentials.TransportCredentials
	certCache *certCache
	caPool    *ClientCAPool
	tracker   *externalConnTracker
	limiter   *x509HandshakeLimiter

	// genByConn records, per in-flight handshake, the CA generation offered as
	// ClientCAs -- known only inside GetConfigForClient -- keyed by the connection.
	genByConn sync.Map // net.Conn -> uint64
}

// NewExternalTransportCredentials builds the external listener's transport credentials,
// serving the certificate found in certDir and optionally verifying client certificates
// against caPool. Certificate-authenticated connections are registered with tracker.
func NewExternalTransportCredentials(certDir string, caPool *ClientCAPool, tracker *externalConnTracker) credentials.TransportCredentials {
	cc := newCertCache(certDir)
	c := &externalTransportCreds{
		certCache: cc,
		caPool:    caPool,
		tracker:   tracker,
		limiter:   newX509HandshakeLimiter(externalMaxConcurrentHandshakes),
	}
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ClientAuth: tls.VerifyClientCertIfGiven,
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			cert, err := cc.get()
			if err != nil {
				return nil, err
			}
			var pool *x509.CertPool
			var generation uint64
			if caPool != nil {
				pool, generation = caPool.Snapshot()
			}
			if hello.Conn != nil {
				c.genByConn.Store(hello.Conn, generation)
			}
			return &tls.Config{
				Certificates: []tls.Certificate{*cert},
				ClientCAs:    pool,
				ClientAuth:   tls.VerifyClientCertIfGiven,
				MinVersion:   tls.VersionTLS12,
			}, nil
		},
	}
	c.TransportCredentials = credentials.NewTLS(tlsCfg)
	return c
}

func (c *externalTransportCreds) ServerHandshake(rawConn net.Conn) (net.Conn, credentials.AuthInfo, error) {
	if !c.limiter.TryAcquire() {
		_ = rawConn.Close()
		return nil, nil, fmt.Errorf("external listener: too many concurrent TLS handshakes")
	}
	defer c.limiter.Release()

	if err := rawConn.SetDeadline(time.Now().Add(externalTLSHandshakeTimeout)); err != nil {
		return nil, nil, err
	}

	conn, authInfo, err := c.TransportCredentials.ServerHandshake(rawConn)
	genVal, _ := c.genByConn.LoadAndDelete(rawConn)
	if err != nil {
		return conn, authInfo, err
	}
	// The handshake deadline must not bound the RPCs that follow.
	if derr := rawConn.SetDeadline(time.Time{}); derr != nil {
		return conn, authInfo, derr
	}

	ic, _ := rawConn.(*idleConn)
	tlsInfo, ok := authInfo.(credentials.TLSInfo)
	if !ok {
		return conn, authInfo, nil
	}

	var tp *TransportPrincipal
	if chains := tlsInfo.State.VerifiedChains; len(chains) > 0 {
		verified := chains[0]
		resp, userOk, uerr := x509req.CommonNameUserConversion.User(verified)
		if uerr != nil || !userOk {
			clog.Debugf(context.Background(), "external tls: certificate identity extraction failed: %v", uerr)
		} else {
			generation, _ := genVal.(uint64)
			tp = &TransportPrincipal{
				Principal:    principalFromUserInfo(resp.User),
				CAGeneration: generation,
				NotAfter:     earliestNotAfter(verified),
			}
			conn = c.tracker.track(conn, generation)
		}
	}
	return conn, &externalAuthInfo{TLSInfo: tlsInfo, TransportPrincipal: tp, idle: ic}, nil
}

func (c *externalTransportCreds) Clone() credentials.TransportCredentials {
	return &externalTransportCreds{
		TransportCredentials: c.TransportCredentials.Clone(),
		certCache:            c.certCache,
		caPool:               c.caPool,
		tracker:              c.tracker,
		limiter:              c.limiter,
	}
}
