package main

import (
	"bytes"
	cryptorand "crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// The program creates the crt.pem, key.pem, and ca.pem needed when
// setting up the mutator webhook for agent auto-injection.
func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "usage: %s <manager-namespace> <directory>", os.Args[0])
		os.Exit(1)
	}
	if err := generateKeys(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func generateKeys(mgrNamespace, dir string) error {
	err := os.MkdirAll(dir, 0o777)
	if err != nil {
		return fmt.Errorf("failed to create directory %q: %w", dir, err)
	}
	crtPem, keyPem, caPem, err := generateKeyTriplet(mgrNamespace)
	if err != nil {
		return err
	}

	if err = writeFile(dir, "ca.pem", caPem); err != nil {
		return err
	}

	if err = writeFile(dir, "crt.pem", crtPem); err != nil {
		return err
	}
	return writeFile(dir, "key.pem", keyPem)
}

// writeFile writes the file verbatim and as base64 encoded in the given directory.
func writeFile(dir, file string, data []byte) error {
	filePath := filepath.Join(dir, file)
	f, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("failed to create file %q, %w", filePath, err)
	}
	defer f.Close()

	if _, err = f.Write(data); err != nil {
		return fmt.Errorf("failed to write to file %q, %w", filePath, err)
	}

	filePath64 := filePath + ".base64"
	f64, err := os.Create(filePath64)
	if err != nil {
		return fmt.Errorf("failed to create file %q, %w", filePath64, err)
	}
	defer f64.Close()

	enc := base64.NewEncoder(base64.StdEncoding, f64)
	defer enc.Close()
	if _, err = enc.Write(data); err != nil {
		return fmt.Errorf("failed to write to file %q, %w", filePath64, err)
	}
	return nil
}

// generateKeyTriplet creates the crt.pem, key.pem, and ca.pem needed when
// setting up the mutator webhook for agent auto-injection.
func generateKeyTriplet(mgrNamespace string) (crtPem, keyPem, caPem []byte, err error) {
	caCert := &x509.Certificate{
		SerialNumber: big.NewInt(0xefecab0),
		Subject: pkix.Name{
			Organization: []string{"getambassador.io"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(1, 0, 0),
		IsCA:                  true,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}

	caPrivKey, err := rsa.GenerateKey(cryptorand.Reader, 4096)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to generate CA private key: %w", err)
	}
	caBytes, err := x509.CreateCertificate(cryptorand.Reader, caCert, caCert, &caPrivKey.PublicKey, caPrivKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to generate CA certificate: %w", err)
	}
	if caPem, err = ToPEM("ca.pem", "CERTIFICATE", caBytes); err != nil {
		return nil, nil, nil, err
	}

	commonName := fmt.Sprintf("agent-injector.%s.svc", mgrNamespace)
	dnsNames := []string{"agent-injector", "agent-injector." + mgrNamespace, commonName}

	// server cert config
	cert := &x509.Certificate{
		DNSNames:     dnsNames,
		SerialNumber: big.NewInt(0xefecab1),
		Subject: pkix.Name{
			CommonName:   commonName,
			Organization: []string{"getambassador.io"},
		},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().AddDate(10, 0, 0), // Valid 10 years
		SubjectKeyId: bigIntHash(caPrivKey.N),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}

	serverPrivateKey, err := rsa.GenerateKey(cryptorand.Reader, 4096)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to server private key: %w", err)
	}

	serverCert, err := x509.CreateCertificate(cryptorand.Reader, cert, caCert, &serverPrivateKey.PublicKey, caPrivKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to sign the server certificate: %w", err)
	}

	if keyPem, err = ToPEM("key.pem", "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(serverPrivateKey)); err != nil {
		return nil, nil, nil, err
	}
	if crtPem, err = ToPEM("crt.pem", "CERTIFICATE", serverCert); err != nil {
		return nil, nil, nil, err
	}
	return crtPem, keyPem, caPem, nil
}

func bigIntHash(n *big.Int) []byte {
	h := sha1.New()
	_, _ = h.Write(n.Bytes())
	return h.Sum(nil)
}

// ToPEM returns the PEM encoding of data.
func ToPEM(file, keyType string, data []byte) ([]byte, error) {
	wrt := bytes.Buffer{}
	if err := pem.Encode(&wrt, &pem.Block{Type: keyType, Bytes: data}); err != nil {
		return nil, fmt.Errorf("failed to PEM encode %s %s: %w", keyType, file, err)
	}
	return wrt.Bytes(), nil
}
