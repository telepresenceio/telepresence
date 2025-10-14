package tls

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	core "k8s.io/api/core/v1"

	"github.com/datawire/dlib/dlog"
)

type portConfig struct {
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

func (p *portConfig) watchPaths(ctx context.Context, certsReady *sync.WaitGroup, setUseTLS func()) (err error) {
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

	setUseTLS()
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
