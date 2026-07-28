package k8s

import (
	"context"
	"crypto/tls"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"k8s.io/client-go/transport"

	"github.com/telepresenceio/clog"
)

const (
	// x509TokenSourceDefaultTTL is used to cache a token that carries no
	// expiration timestamp.
	x509TokenSourceDefaultTTL = 10 * time.Minute

	// x509TokenSourceMaxTTL clamps a server-supplied expiration so a
	// misbehaving manager cannot mint a token that is cached far into the
	// future.
	x509TokenSourceMaxTTL = time.Hour

	// x509ExchangeTimeout bounds the entire dial+handshake+read exchange,
	// independent of the caller's own context deadline.
	x509ExchangeTimeout = 10 * time.Second

	// x509ResponseSizeLimit bounds how much of the manager's response is
	// read before the exchange is rejected.
	x509ResponseSizeLimit = 16 * 1024
)

// x509AuthDialer has the same shape as the func returned by
// portforward.Dialer, so the exchange rides the same shared per-pod stream
// connection as the manager gRPC channel.
type x509AuthDialer func(ctx context.Context, address string) (net.Conn, error)

// x509TokenSource yields short-lived bearer tokens for use as per-RPC
// "authorization" metadata, minted by the traffic-manager in exchange for a
// one-shot TLS handshake presenting the kubeconfig's client certificate.
//
// Token returns ("", nil) — no credential — until activate has supplied the
// dial function and the address of the manager's auth listener. Once
// activated, Token caches the minted token and performs a fresh handshake
// when it expires.
type x509TokenSource struct {
	tlsConfig *tls.Config

	mu     sync.Mutex
	dial   x509AuthDialer
	addr   string
	active bool

	cached bool
	expiry time.Time
	token  string

	// exchangeGate is a capacity-1 semaphore that serializes handshakes;
	// waiters reuse the finished exchange's result instead of dialing again.
	// Lazily created so the zero value needs no constructor.
	exchangeGate chan struct{}
}

// newX509TokenSource returns a deactivated source that will authenticate
// with kc's client certificate, or nil when kc has no client-certificate
// credentials at all (a plain bearer-token or auth-provider kubeconfig).
func newX509TokenSource(kc *Kubeconfig) *x509TokenSource {
	if kc == nil || kc.RestConfig == nil {
		return nil
	}
	tc, err := kc.RestConfig.TransportConfig()
	if err != nil {
		clog.Debugf(kc, "manager x509 auth: unable to build a transport config from the kubeconfig: %v", err)
		return nil
	}
	if !(tc.HasCertAuth() || tc.HasCertCallback()) {
		return nil
	}
	tlsConfig, err := transport.TLSConfigFor(tc)
	if err != nil || tlsConfig == nil {
		clog.Debugf(kc, "manager x509 auth: unable to build a TLS client config from the kubeconfig: %v", err)
		return nil
	}
	// The manager's server certificate for this handshake is ephemeral
	// self-signed; server authenticity is already established by the API
	// server routing the port-forward to the resolved pod, so the client
	// does not need to (and cannot) verify it.
	tlsConfig.InsecureSkipVerify = true
	tlsConfig.MinVersion = tls.VersionTLS13
	return &x509TokenSource{tlsConfig: tlsConfig}
}

// activate supplies the dial function and the pinned pod address of the
// manager's auth listener, learned from the manager's version handshake,
// enabling Token to perform the exchange.
func (s *x509TokenSource) activate(dial x509AuthDialer, addr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dial = dial
	s.addr = addr
	s.active = true
	s.cached = false
}

// x509AuthResponse mirrors the single JSON object the manager's auth
// listener writes before closing the connection.
type x509AuthResponse struct {
	Token               string `json:"token"`
	ExpirationTimestamp string `json:"expirationTimestamp"`
	Error               string `json:"error"`
}

func (s *x509TokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	active := s.active
	if s.exchangeGate == nil {
		s.exchangeGate = make(chan struct{}, 1)
	}
	gate := s.exchangeGate
	if token, ok := s.cachedLocked(); ok {
		s.mu.Unlock()
		return token, nil
	}
	s.mu.Unlock()
	if !active {
		return "", nil
	}

	// Bound the entire exchange, including any wait behind an in-flight
	// handshake below, independent of the caller's own context deadline.
	ctx, cancel := context.WithTimeout(ctx, x509ExchangeTimeout)
	defer cancel()

	// Only one handshake runs at a time; a caller that arrives while one
	// is in flight waits for it here instead of dialing concurrently. A
	// caller whose context expires while waiting gives up on the wait
	// instead of hanging until the in-flight exchange happens to finish.
	select {
	case gate <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-gate }()

	s.mu.Lock()
	if token, ok := s.cachedLocked(); ok {
		s.mu.Unlock()
		return token, nil
	}
	dial, addr := s.dial, s.addr
	s.mu.Unlock()

	token, expiry, err := s.handshake(ctx, dial, addr)

	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.cached = false
		return "", err
	}
	s.token, s.expiry, s.cached = token, expiry, true
	return token, nil
}

// cachedLocked returns the cached token and true when the source is active
// and the token has not yet expired. Callers must hold s.mu.
func (s *x509TokenSource) cachedLocked() (string, bool) {
	if s.active && s.cached && time.Now().Before(s.expiry) {
		return s.token, true
	}
	return "", false
}

// handshake dials the manager's auth port on the pinned pod, presents the
// client certificate, and reads the single JSON response the manager writes
// before closing the connection. No application data is sent. ctx is already
// bounded by the caller (Token). It does not touch s.mu: state is read and
// written by the caller before and after this call.
func (s *x509TokenSource) handshake(ctx context.Context, dial x509AuthDialer, addr string) (string, time.Time, error) {
	rawConn, err := dial(ctx, addr)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("dial manager x509 auth port: %w", err)
	}
	conn := tls.Client(rawConn, s.tlsConfig)
	defer conn.Close()

	// The port-forward net.Conn's SetDeadline can be a silent no-op when
	// the underlying httpstream does not implement net.Conn, so a
	// watchdog closes the connection directly when ctx ends.
	watchdogDone := make(chan struct{})
	defer close(watchdogDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-watchdogDone:
		}
	}()

	if err := conn.HandshakeContext(ctx); err != nil {
		return "", time.Time{}, fmt.Errorf("x509 handshake with manager auth port: %w", err)
	}
	body, err := io.ReadAll(io.LimitReader(conn, x509ResponseSizeLimit+1))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("read manager x509 auth response: %w", err)
	}
	if len(body) > x509ResponseSizeLimit {
		return "", time.Time{}, fmt.Errorf("manager x509 auth response exceeds %d bytes", x509ResponseSizeLimit)
	}
	var resp x509AuthResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", time.Time{}, fmt.Errorf("parse manager x509 auth response: %w", err)
	}
	if resp.Error != "" {
		return "", time.Time{}, fmt.Errorf("manager x509 authentication failed: %s", resp.Error)
	}
	if resp.Token == "" {
		return "", time.Time{}, errors.New("manager x509 auth response carried no token")
	}
	expiry := time.Now().Add(x509TokenSourceDefaultTTL)
	if resp.ExpirationTimestamp != "" {
		if t, err := time.Parse(time.RFC3339, resp.ExpirationTimestamp); err == nil {
			expiry = t.Add(-execTokenSafetyMargin)
		}
	}
	if maxExpiry := time.Now().Add(x509TokenSourceMaxTTL); expiry.After(maxExpiry) {
		expiry = maxExpiry
	}
	return resp.Token, expiry, nil
}
