package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"slices"
	"strconv"
	"sync"
	"time"

	x509req "k8s.io/apiserver/pkg/authentication/request/x509"
	"k8s.io/apiserver/pkg/authentication/user"

	"github.com/telepresenceio/clog"
)

// x509HandshakeTimeout bounds the entire per-connection exchange: TLS handshake,
// verification, and writing the response.
const x509HandshakeTimeout = 10 * time.Second

// x509ServerCertValidity is the lifetime of the listener's own ephemeral server
// certificate, regenerated every time the manager starts and never persisted. The
// client skips verification of it: server authenticity is already provided by the
// API server routing the port-forward to the selected manager pod.
const x509ServerCertValidity = 10 * 365 * 24 * time.Hour

// x509MaxConcurrentHandshakes bounds the number of TLS handshakes in flight, so a burst
// of connections cannot exhaust goroutines or CPU on handshake crypto. Connections
// beyond the cap are closed immediately, before any handshake I/O.
const x509MaxConcurrentHandshakes = 16

// x509RejectWarnInterval bounds how often a Warn-level summary of rejected client
// certificates is logged, so a burst of invalid connections cannot flood the log.
// Individual rejections are still logged at Debug.
const x509RejectWarnInterval = 10 * time.Second

// X509Listener is the manager's auth-only TLS listener: a client presents its
// kubeconfig client certificate over a one-shot TLS handshake and, once the
// certificate verifies against the cluster client CA, receives a short-lived opaque
// bearer token that authenticates it on the regular gRPC channel.
type X509Listener struct {
	ln      net.Listener
	caPool  *ClientCAPool
	tokens  *MintedTokens
	tlsCfg  *tls.Config
	limiter *x509HandshakeLimiter
	rejects rejectLog
}

// NewX509Listener starts the auth-only TLS listener on 0.0.0.0:port.
func NewX509Listener(port uint16, caPool *ClientCAPool, tokens *MintedTokens) (*X509Listener, error) {
	cert, err := newX509ServerCert()
	if err != nil {
		return nil, err
	}
	addr := net.JoinHostPort("0.0.0.0", strconv.Itoa(int(port)))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("x509 auth: listen on %s: %w", addr, err)
	}
	return &X509Listener{
		ln:      ln,
		caPool:  caPool,
		tokens:  tokens,
		limiter: newX509HandshakeLimiter(x509MaxConcurrentHandshakes),
		tlsCfg: &tls.Config{
			Certificates: []tls.Certificate{cert},
			ClientAuth:   tls.RequireAnyClientCert,
			MinVersion:   tls.VersionTLS13,
		},
	}, nil
}

func newX509ServerCert() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("x509 auth: generate server key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("x509 auth: generate certificate serial: %w", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "telepresence-traffic-manager-x509-auth"},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.Add(x509ServerCertValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("x509 auth: create server certificate: %w", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

// Addr returns the listener's local address.
func (l *X509Listener) Addr() net.Addr {
	return l.ln.Addr()
}

// Close closes the underlying listener without waiting for accepted connections to
// drain.
func (l *X509Listener) Close() error {
	return l.ln.Close()
}

// Serve runs the accept loop until ctx is done or the listener is otherwise closed, at
// which point it returns nil. A problem with an individual connection is logged and
// never terminates Serve.
func (l *X509Listener) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		_ = l.ln.Close()
	}()
	for {
		conn, err := l.ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			clog.Errorf(ctx, "x509 auth: accept failed: %v", err)
			continue
		}
		if !l.limiter.TryAcquire() {
			_ = conn.Close()
			continue
		}
		go func() {
			defer l.limiter.Release()
			l.handleConn(ctx, conn)
		}()
	}
}

func (l *X509Listener) handleConn(ctx context.Context, raw net.Conn) {
	conn := tls.Server(raw, l.tlsCfg)
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(x509HandshakeTimeout)); err != nil {
		clog.Errorf(ctx, "x509 auth: set deadline for %s: %v", raw.RemoteAddr(), err)
		return
	}

	// exchangeCtx bounds the handshake and the verify/reload path that follows it, so
	// a stalled Kubernetes API server (see ClientCAPool.reload) cannot pin this
	// handshake slot for longer than x509HandshakeTimeout; the socket deadline above
	// bounds the raw connection I/O the same way.
	exchangeCtx, cancel := context.WithTimeout(ctx, x509HandshakeTimeout)
	defer cancel()

	if err := conn.HandshakeContext(exchangeCtx); err != nil {
		clog.Debugf(ctx, "x509 auth: TLS handshake from %s failed: %v", raw.RemoteAddr(), err)
		return
	}
	chain := conn.ConnectionState().PeerCertificates
	p, notAfter, generation, err := l.verify(exchangeCtx, chain)
	if err != nil {
		l.rejects.reject(ctx, raw.RemoteAddr().String(), err)
		writeJSONResponse(ctx, conn, map[string]string{"error": err.Error()})
		return
	}
	fp := sha256.Sum256(chain[0].Raw)
	fpHex := hex.EncodeToString(fp[:])

	token, expiresAt, err := l.tokens.Mint(p, fpHex, notAfter, generation)
	if errors.Is(err, ErrCAGenerationMismatch) {
		// The CA pool was swapped -- invalidating every minted token -- between
		// verify and Mint. Re-verify once against the now-current snapshot, since
		// the certificate may be valid under the new bundle too, and retry.
		var verr error
		p, notAfter, generation, verr = l.verify(exchangeCtx, chain)
		if verr != nil {
			l.rejects.reject(ctx, raw.RemoteAddr().String(), verr)
			writeJSONResponse(ctx, conn, map[string]string{"error": verr.Error()})
			return
		}
		token, expiresAt, err = l.tokens.Mint(p, fpHex, notAfter, generation)
	}
	if err != nil {
		if errors.Is(err, ErrCAGenerationMismatch) {
			l.rejects.reject(ctx, raw.RemoteAddr().String(), err)
			writeJSONResponse(ctx, conn, map[string]string{"error": err.Error()})
			return
		}
		clog.Errorf(ctx, "x509 auth: unable to mint token for %s: %v", p.Username, err)
		writeJSONResponse(ctx, conn, map[string]string{"error": "internal error"})
		return
	}
	writeJSONResponse(ctx, conn, map[string]string{
		"token":               token,
		"expirationTimestamp": expiresAt.UTC().Format(time.RFC3339),
	})
}

// verify checks chain against the current CA pool snapshot, retrying once against
// a freshly reloaded snapshot on failure. It returns the generation of the snapshot
// verified against, for Mint's generation check.
func (l *X509Listener) verify(ctx context.Context, chain []*x509.Certificate) (*Principal, time.Time, uint64, error) {
	pool, generation := l.caPool.Snapshot()
	p, notAfter, err := l.verifyAgainst(pool, chain)
	if err == nil {
		return p, notAfter, generation, nil
	}
	l.caPool.ReloadOnFailure(ctx)
	pool, generation = l.caPool.Snapshot()
	p, notAfter, err = l.verifyAgainst(pool, chain)
	return p, notAfter, generation, err
}

// verifyAgainst verifies chain's leaf against pool and converts the verified chain
// into a Principal with the same identity the API server's own x509 authenticator
// would assign. The returned time.Time is the earliest NotAfter across the chain.
func (l *X509Listener) verifyAgainst(pool *x509.CertPool, chain []*x509.Certificate) (*Principal, time.Time, error) {
	if len(chain) == 0 {
		return nil, time.Time{}, errors.New("no client certificate presented")
	}
	if pool == nil {
		return nil, time.Time{}, errors.New("no client CA configured")
	}
	leaf := chain[0]
	intermediates := x509.NewCertPool()
	for _, c := range chain[1:] {
		intermediates.AddCert(c)
	}
	opts := x509.VerifyOptions{
		Roots:         pool,
		Intermediates: intermediates,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	chains, err := leaf.Verify(opts)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("certificate verification failed: %w", err)
	}
	verified := chains[0]
	resp, ok, err := x509req.CommonNameUserConversion.User(verified)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("certificate identity extraction failed: %w", err)
	}
	if !ok {
		return nil, time.Time{}, errors.New("certificate has no CommonName")
	}
	return principalFromUserInfo(resp.User), earliestNotAfter(verified), nil
}

// principalFromUserInfo converts a user.Info into a Principal, adding the
// system:authenticated group the way the API server does.
func principalFromUserInfo(info user.Info) *Principal {
	groups := slices.Clone(info.GetGroups())
	name := info.GetName()
	if name != user.Anonymous && !slices.Contains(groups, user.AllAuthenticated) && !slices.Contains(groups, user.AllUnauthenticated) {
		groups = append(groups, user.AllAuthenticated)
	}
	p := &Principal{
		Username: name,
		UID:      info.GetUID(),
		Groups:   groups,
	}
	if extra := info.GetExtra(); len(extra) > 0 {
		p.Extra = extra
	}
	return p
}

// earliestNotAfter returns the earliest NotAfter across chain, which starts with the
// leaf certificate and ends with a certificate in the trust root.
func earliestNotAfter(chain []*x509.Certificate) time.Time {
	na := chain[0].NotAfter
	for _, c := range chain[1:] {
		if c.NotAfter.Before(na) {
			na = c.NotAfter
		}
	}
	return na
}

// x509HandshakeLimiter caps the number of concurrent TLS handshakes, implemented as a
// buffered channel used as a counting semaphore.
type x509HandshakeLimiter struct {
	sem chan struct{}
}

func newX509HandshakeLimiter(limit int) *x509HandshakeLimiter {
	return &x509HandshakeLimiter{sem: make(chan struct{}, limit)}
}

// TryAcquire reports whether a handshake slot was obtained without blocking. Release
// must be called exactly once for every TryAcquire that returned true.
func (l *x509HandshakeLimiter) TryAcquire() bool {
	select {
	case l.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

func (l *x509HandshakeLimiter) Release() {
	<-l.sem
}

// rejectLog logs each rejection at Debug, and additionally at most one Warn every
// x509RejectWarnInterval summarizing how many rejections occurred since the last one.
type rejectLog struct {
	mu       sync.Mutex
	count    int
	lastWarn time.Time
}

func (r *rejectLog) reject(ctx context.Context, remote string, err error) {
	clog.Debugf(ctx, "x509 auth: rejecting client %s: %v", remote, err)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.count++
	if time.Since(r.lastWarn) >= x509RejectWarnInterval {
		clog.Warnf(ctx, "x509 auth: rejected %d client certificate(s) in the last %s", r.count, x509RejectWarnInterval)
		r.count = 0
		r.lastWarn = time.Now()
	}
}

// writeJSONResponse writes a single JSON object to conn and logs, without including v,
// if the write fails.
func writeJSONResponse(ctx context.Context, conn net.Conn, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		clog.Errorf(ctx, "x509 auth: marshal response: %v", err)
		return
	}
	if _, err := conn.Write(b); err != nil {
		clog.Debugf(ctx, "x509 auth: write response to %s failed: %v", conn.RemoteAddr(), err)
	}
}
