// Command h3server is a minimal in-cluster HTTP/3 server used by the datagram-carriage
// experiment (perf/network/datagram_test.go)
// to measure RFC 9221 datagram carriage's head-of-line benefit for tunneled UDP: the
// experiment's inner transport (HTTP/3, itself UDP-based) rides as a UDP flow through the
// telepresence tunnel, so a lost tunnel-carrier packet exercises exactly the datagram-vs
// -stream carriage path that pkg/tunnel/datagram.go implements.
//
// It serves one fixed, randomly generated (incompressible) payload at GET /payload.bin,
// honoring Range requests the same way the nginx-backed perf-payload workload
// (testdata/payload.yaml) does for the head-of-line experiment, and presents a self-signed TLS
// certificate generated at startup -- there is no cluster CA to hand it one, and the perf
// client connects with InsecureSkipVerify, so a self-signed cert is sufficient.
package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/quic-go/quic-go/http3"
)

func main() {
	addr := envOr("LISTEN_ADDR", ":443")
	payloadBytes := envInt("PAYLOAD_BYTES", 8*1024*1024)

	payload := make([]byte, payloadBytes)
	if _, err := rand.Read(payload); err != nil {
		log.Fatalf("generate payload: %v", err)
	}

	cert, err := selfSignedCert()
	if err != nil {
		log.Fatalf("generate TLS certificate: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/payload.bin", func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "payload.bin", time.Time{}, bytes.NewReader(payload))
	})

	srv := &http3.Server{
		Addr:      addr,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}},
		Handler:   mux,
	}
	log.Printf("perf-h3: listening on %s (payload %d bytes)", addr, payloadBytes)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("serve: %v", err)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// tlsDNSNames is the default set of names the perf-h3 Service (testdata/h3server.yaml) is
// reachable as from inside the cluster; TLS_DNS_NAMES overrides it (comma-separated) if the
// Service is ever installed under a different name or namespace.
const tlsDNSNames = "perf-h3,perf-h3.default,perf-h3.default.svc,perf-h3.default.svc.cluster.local,localhost"

// selfSignedCert generates a fresh self-signed ECDSA certificate covering the Service's
// usual in-cluster names plus this pod's own IP (from the downward API, POD_IP), so a
// client that does bother to verify the chain against a name still finds a match; the perf
// client does not (InsecureSkipVerify), but a test fixture should not depend on that.
func selfSignedCert() (tls.Certificate, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "perf-h3"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              strings.Split(envOr("TLS_DNS_NAMES", tlsDNSNames), ","),
	}
	if ip := net.ParseIP(os.Getenv("POD_IP")); ip != nil {
		tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	)
}
