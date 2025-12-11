package tls

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/fsnotify/fsnotify"
	"golang.org/x/net/http2"
	core "k8s.io/api/core/v1"

	"github.com/datawire/dlib/dlog"
)

type ValueState int32

const (
	ValueUnknown ValueState = iota
	ValueNotSupported
	ValueSupported
)

type portConfig struct {
	// The port number that this configuration applies to.
	port uint16

	// The path to the secret containing the downstream TLS certificate, as it is mounted into the app-container. The
	// path is subjected to environment variable expansion, using the same rules as the app-container.
	// The agent adds the environment variable WORKLOAD_NAME.
	downstreamSecretPath string

	// The path to the secret containing the upstream TLS certificate, as it is mounted into the app-container. The
	// path is subjected to environment variable expansion, using the same rules as the app-container.
	// The agent adds the environment variable WORKLOAD_NAME.
	upstreamSecretPath string

	containerName string

	certMutex                  sync.Mutex
	downstreamCertificate      *tls.Certificate
	upstreamCertificate        *tls.Certificate
	upstreamInsecureSkipVerify bool
	upstreamProbeTimeout       time.Duration

	portMutex sync.Mutex
	TLS       ValueState
	HTTP2     ValueState
}

func newPortConfig(port uint16, containerName string) *portConfig {
	return &portConfig{
		port:                 port,
		containerName:        containerName,
		upstreamProbeTimeout: defaultProbeTimeout,
		TLS:                  ValueUnknown,
		HTTP2:                ValueUnknown,
	}
}

func pathExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return !errors.Is(err, os.ErrNotExist)
}

const (
	istioCertKey    = "cert-chain.pem"
	istioPrivateKey = "key.pem"
)

func makeContainerPath(containerName, path string) (string, error) {
	clean := filepath.Clean(path)
	if strings.Contains(clean, "..") {
		return "", fmt.Errorf("path %q contains '..'", path)
	}
	return filepath.Join("/tel_app_mounts", containerName, clean), nil
}

func (p *portConfig) setTLS(ctx context.Context, tls ValueState) {
	s := "supports"
	if tls != ValueSupported {
		s = "does not support"
	}
	dlog.Debugf(ctx, "Port %d %s TLS", p.port, s)
	p.TLS = tls
}

func (p *portConfig) setHTTP2(ctx context.Context, http2 ValueState) {
	s := "supports"
	if http2 != ValueSupported {
		s = "does not support"
	}
	dlog.Debugf(ctx, "Port %d %s HTTP/2", p.port, s)
	p.HTTP2 = http2
}

func (p *portConfig) probeWarning(ctx context.Context, what string, err error) {
	dlog.Warnf(ctx, "Failed to probe port %d for %s support: %v. "+
		"To avoid probing and improve startup time, add 'appProtocol: https' "+
		"(or 'appProtocol: h2c' for HTTP/2 cleartext) to your Service definition. "+
		"See: https://kubernetes.io/docs/concepts/services-networking/service/#application-protocol",
		p.port, what, err)
}

func (p *portConfig) probeTLS(ctx context.Context, podIP netip.Addr) bool {
	p.portMutex.Lock()
	supported := p.probeTLSWithLock(ctx, podIP)
	p.portMutex.Unlock()
	return supported
}

func (p *portConfig) probeTLSWithLock(ctx context.Context, podIP netip.Addr) bool {
	if p.TLS != ValueUnknown && p.HTTP2 != ValueUnknown {
		return p.TLS == ValueSupported
	}
	port := p.port
	dlog.Debugf(ctx, "Probing port %d for TLS and HTTP/2 support", port)
	addr := netip.AddrPortFrom(podIP, port).String()
	bc := backoff.NewExponentialBackOff()
	bc.MaxElapsedTime = p.upstreamProbeTimeout
	bc.MaxInterval = 300 * time.Millisecond
	bc.InitialInterval = 100 * time.Millisecond
	err := backoff.Retry(func() error {
		ctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		defer cancel()
		dialer := &net.Dialer{}
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			dlog.Debugf(ctx, "Unable to dial %s: %v", addr, err)
			return err
		}
		defer conn.Close()

		tlsConn := tls.Client(conn, &tls.Config{
			NextProtos:         []string{"h2", "http/1.1"},
			InsecureSkipVerify: true,
		})

		if err = tlsConn.HandshakeContext(ctx); err != nil {
			dlog.Debugf(ctx, "Port %d does not support TLS: %v", port, err)
			p.TLS = ValueNotSupported
		} else {
			p.setTLS(ctx, ValueSupported)
			state := ValueNotSupported
			if tlsConn.ConnectionState().NegotiatedProtocol == "h2" {
				state = ValueSupported
			}
			p.setHTTP2(ctx, state)
		}
		return nil
	}, backoff.WithContext(bc, ctx))
	if err != nil {
		p.probeWarning(ctx, "TLS and HTTP/2", err)
	}
	return p.TLS == ValueSupported
}

func (p *portConfig) probeHTTP2(ctx context.Context, podIP netip.Addr) bool {
	p.portMutex.Lock()
	defer p.portMutex.Unlock()
	if p.HTTP2 != ValueUnknown {
		return p.HTTP2 == ValueSupported
	}

	port := p.port
	if p.TLS == ValueNotSupported {
		dlog.Debugf(ctx, "Probing port %d for HTTP/2 clear-text support", port)
		state := ValueNotSupported
		if p.probeHTTP2ClearTextWithLock(ctx) {
			state = ValueSupported
		}
		p.setHTTP2(ctx, state)
	} else {
		// TLS has been determined from annotation or appProtocol because otherwise the HTTP/2 status would already be known.
		// Let's probe TLS to also get HTTP/2 status.
		p.probeTLSWithLock(ctx, podIP)
	}
	return p.HTTP2 == ValueSupported
}

func (p *portConfig) probeHTTP2ClearTextWithLock(ctx context.Context) bool {
	bc := backoff.NewExponentialBackOff()
	bc.MaxElapsedTime = p.upstreamProbeTimeout
	bc.MaxInterval = 300 * time.Millisecond
	bc.InitialInterval = 100 * time.Millisecond
	var conn net.Conn
	err := backoff.Retry(func() error {
		dialer := &net.Dialer{Timeout: 200 * time.Millisecond}
		var err error
		conn, err = dialer.Dial("tcp", fmt.Sprintf(":%d", p.port))
		return err
	}, backoff.WithContext(bc, ctx))
	if err != nil {
		p.probeWarning(ctx, "HTTP/2 cleartext", err)
		return false
	}
	defer conn.Close()

	// Set a read and write deadline for the initial SETTINGS frame. Shouldn't take long given that
	// the connection is already established. If it does, then something is wrong, and we'll just give
	// up and return false.
	deadline := time.Now().Add(200 * time.Millisecond)
	if err := conn.SetDeadline(deadline); err != nil {
		dlog.Debugf(ctx, "failed to set connection deadline on port %d: %v", p.port, err)
		return false
	}

	// HTTP/2 connection preface for prior knowledge (direct h2c).
	// Send the preface.
	port := p.port
	_, err = conn.Write([]byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"))
	if err != nil {
		dlog.Debugf(ctx, "failed to send connection preface on port %d: %v", port, err)
		return false
	}
	// Send the required settings frame (empty).
	_, err = conn.Write([]byte{0, 0, 0, byte(http2.FrameSettings), 0, 0, 0, 0, 0})
	if err != nil {
		dlog.Debugf(ctx, "failed to send empty settings frame on port %d: %v", port, err)
		return false
	}

	// Read the initial response (expect SETTINGS frame).
	buf := make([]byte, 9) // Frame header is 9 bytes.
	n, err := conn.Read(buf)
	if err != nil || n != 9 {
		dlog.Debugf(ctx, "failed to read settings frame on port %d: %v", port, err)
		return false
	}

	// Parse as HTTP/2 frame header.
	fr, err := http2.ReadFrameHeader(bytes.NewReader(buf))
	if err != nil {
		dlog.Debugf(ctx, "invalid frame header: %v", err)
		return false
	}

	// Check if it's a valid, but empty SETTINGS frame with no flags.
	if fr.Type != http2.FrameSettings || fr.Flags != 0 {
		dlog.Debugf(ctx, "expected SETTINGS frame and empty flags, got %v, %b", fr.Type, fr.Flags)
		return false
	}

	// The frame payload should be empty for initial SETTINGS.
	if fr.Length > 0 {
		buf = make([]byte, fr.Length)
		n, err = conn.Read(buf)
		if err != nil || n != int(fr.Length) {
			dlog.Debugf(ctx, "failed to read settings payload on port %d: %v", port, err)
			return false
		} else {
			dlog.Debugf(ctx, "SETTINGS payload %s", hex.Dump(buf))
		}
	}
	_, err = conn.Write([]byte{0, 0, 0, byte(http2.FrameSettings), byte(http2.FlagSettingsAck), 0, 0, 0, 0})
	if err != nil {
		dlog.Debugf(ctx, "failed to write ack settings frame on port %d: %v", port, err)
	}
	return err == nil
}

func (p *portConfig) certPaths(containerName string) (dsPath, usPath string, err error) {
	if p == nil {
		return "", "", nil
	}
	if p.downstreamSecretPath != "" {
		dsPath, err = makeContainerPath(containerName, p.downstreamSecretPath)
	}
	if err == nil && p.upstreamSecretPath != "" {
		usPath, err = makeContainerPath(containerName, p.upstreamSecretPath)
	}
	return dsPath, usPath, err
}

func (p *portConfig) getDownstreamCert() *tls.Certificate {
	if p == nil {
		return nil
	}
	p.certMutex.Lock()
	cert := p.downstreamCertificate
	p.certMutex.Unlock()
	return cert
}

func (p *portConfig) getUpstreamCert() (*tls.Certificate, bool) {
	if p == nil {
		return nil, false
	}
	p.certMutex.Lock()
	cert := p.upstreamCertificate
	skipVerify := p.upstreamInsecureSkipVerify
	p.certMutex.Unlock()
	return cert, skipVerify
}

func (p *portConfig) loadCerts(dsPath, usPath string) error {
	dsCert, err := loadCertFromPath("downstream", dsPath)
	if err != nil {
		return err
	}
	var usCert *tls.Certificate
	if dsPath == usPath {
		usCert = dsCert
	} else {
		usCert, err = loadCertFromPath("upstream", usPath)
		if err != nil {
			return err
		}
	}
	p.certMutex.Lock()
	p.downstreamCertificate = dsCert
	p.upstreamCertificate = usCert
	p.certMutex.Unlock()
	return nil
}

func (p *portConfig) watchPaths(ctx context.Context, certsReady *sync.WaitGroup) (err error) {
	dsPath, usPath, err := p.certPaths(p.containerName)
	if err != nil {
		return err
	}
	eq := dsPath == usPath

	dsExists := pathExists(dsPath)
	if !dsExists && pathExists(p.downstreamSecretPath) {
		// The path was mounted into the traffic-agent itself. This happens when the workload is
		// annotated with a downstream-tls-secret.
		dsPath = p.downstreamSecretPath
		dsExists = true
	}
	usExists := dsExists
	if !eq {
		usExists = pathExists(usPath)
		if !usExists && pathExists(p.upstreamSecretPath) {
			usPath = p.upstreamSecretPath
			usExists = true
		}
	}

	if !dsExists && !usExists {
		// We don't watch certs that didn't exist when we started.
		certsReady.Done()
		return nil
	}

	err = p.loadCerts(dsPath, usPath)
	if err != nil {
		certsReady.Done()
		return fmt.Errorf("failed to load certificates: %w", err)
	}

	p.setTLS(ctx, ValueSupported)
	certsReady.Done()

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to start file watcher: %w", err)
	}
	defer w.Close()

	if dsExists {
		err = w.Add(dsPath)
		if err != nil {
			return fmt.Errorf("unable to watch %s: %w", dsPath, err)
		}
	}
	if !eq && usExists {
		err = w.Add(usPath)
		if err != nil {
			return fmt.Errorf("unable to watch %s: %w", usPath, err)
		}
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-w.Events:
			if !ok {
				return nil
			}
			err = p.loadCerts(dsPath, usPath)
			if err != nil {
				return err
			}
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			return fmt.Errorf("error from watcher: %w", err)
		}
	}
}

func loadCertFromPath(direction, path string) (*tls.Certificate, error) {
	certData, keyData, err := tryCertificate(direction, path, core.TLSCertKey, core.TLSPrivateKeyKey)
	if errors.Is(err, os.ErrNotExist) {
		certData, keyData, err = tryCertificate(direction, path, istioCertKey, istioPrivateKey)
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		} else {
			err = fmt.Errorf("reading %s certificate from %s failed: %w ", direction, path, err)
		}
		return nil, err
	}
	tc, err := tls.X509KeyPair(certData, keyData)
	if err != nil {
		return nil, fmt.Errorf("creation of %s certificate failed: %w ", direction, err)
	}

	// Reparse the certificate to check expiration. No need to check for errors here because the tls.X509KeyPair function already did that.
	x509Cert, _ := x509.ParseCertificate(tc.Certificate[0])

	now := time.Now()
	// Check if the certificate is expired
	if now.After(x509Cert.NotAfter) {
		return nil, fmt.Errorf("%s certificate expired on %s (current time: %s)", direction, x509Cert.NotAfter.Format(time.RFC3339), now.Format(time.RFC3339))
	}

	// Check if the certificate is not yet valid
	if now.Before(x509Cert.NotBefore) {
		return nil, fmt.Errorf("%s certificate not valid until %s (current time: %s)", direction, x509Cert.NotBefore.Format(time.RFC3339), now.Format(time.RFC3339))
	}

	// Warn if the certificate expires soon (within 7 days)
	if now.Add(7 * 24 * time.Hour).After(x509Cert.NotAfter) {
		dlog.Warnf(context.Background(), "%s certificate will expire on %s", direction, x509Cert.NotAfter.Format(time.RFC3339))
	}
	return &tc, nil
}

func loadData(direction, path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("unable to read %s certificate file %q: %w", direction, path, err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("%s certificate file %q is empty", direction, path)
	}
	return data, nil
}

func tryCertificate(direction, path, certKey, privateKey string) (certData, keyData []byte, err error) {
	certPath := filepath.Join(path, certKey)
	certData, err = loadData(direction, certPath)
	if err != nil {
		return nil, nil, err
	}
	privatePath := filepath.Join(path, privateKey)
	keyData, err = loadData(direction, privatePath)
	if err != nil {
		return nil, nil, err
	}
	return certData, keyData, nil
}
