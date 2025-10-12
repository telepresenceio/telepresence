package tls

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/fsnotify/fsnotify"
	core "k8s.io/api/core/v1"
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

func (p *portConfig) certPaths(cn string) (dsPath, usPath string) {
	if p == nil {
		return "", ""
	}
	if p.downstreamSecretPath != "" {
		dsPath = filepath.Join("/tel_app_mounts", cn, p.downstreamSecretPath)
	}
	if p.upstreamSecretPath != "" {
		usPath = filepath.Join("/tel_app_mounts", cn, p.upstreamSecretPath)
	}
	return dsPath, usPath
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
	dsCert, err := loadCertFromPath(dsPath)
	if err != nil {
		return err
	}
	var usCert *tls.Certificate
	if dsPath == usPath {
		usCert = dsCert
	} else {
		usCert, err = loadCertFromPath(usPath)
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
	dsPath, usPath := p.certPaths(p.containerName)
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
	certsReady.Done()
	if err != nil {
		return fmt.Errorf("failed to load certificates: %w", err)
	}

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

func loadCertFromPath(path string) (*tls.Certificate, error) {
	certData, keyData, err := tryCertificate(path, core.TLSCertKey, core.TLSPrivateKeyKey)
	if errors.Is(err, os.ErrNotExist) {
		certData, keyData, err = tryCertificate(path, istioCertKey, istioPrivateKey)
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
		return nil, err
	}
	tc, err := tls.X509KeyPair(certData, keyData)
	if err != nil {
		return nil, err
	}
	return &tc, nil
}

func tryCertificate(path, certKey, privateKey string) (certData, keyData []byte, err error) {
	certPath := filepath.Join(path, certKey)
	certData, err = os.ReadFile(certPath)
	if err != nil {
		return nil, nil, fmt.Errorf("secret %q failed to load: %w", certPath, err)
	}
	privatePath := filepath.Join(path, privateKey)
	keyData, err = os.ReadFile(privatePath)
	if err != nil {
		return nil, nil, fmt.Errorf("secret %q loaded successfully but %q failed with: %w", certPath, privatePath, err)
	}
	return certData, keyData, nil
}
