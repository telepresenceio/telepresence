// Package sessiontoken implements a compact signed bearer token that names a
// Telepresence session. It is minted by the traffic-manager with the QUIC CA's
// ECDSA P-256 private key (cmd/traffic/cmd/manager/quictunnel) and verified offline,
// without a round-trip to the manager, against the CA certificate the verifier
// already holds. See "The credential" in
// docs/plans/auth-hardening/file-sharing-auth.md.
//
// A token rides anywhere a certificate cannot: the FTP PASS command and gRPC
// metadata on a plaintext, port-forwarded channel.
package sessiontoken

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// version is the only token format this package mints or accepts.
const version = "v1"

// signingDomain prefixes the signed data so the CA key's session-token signatures
// can never be confused with a signature over some other format.
const signingDomain = "telepresence-session-token/v1"

// Mint returns a bearer token naming sessionID, signed by key, that Verify accepts
// until expiry.
func Mint(key *ecdsa.PrivateKey, sessionID string, expiry time.Time) (string, error) {
	expirySecs := expiry.Unix()
	d := digest(sessionID, expirySecs)
	sig, err := ecdsa.SignASN1(rand.Reader, key, d[:])
	if err != nil {
		return "", fmt.Errorf("sessiontoken: sign: %w", err)
	}
	return fmt.Sprintf("%s.%s.%d.%s", version, sessionID, expirySecs, base64.RawURLEncoding.EncodeToString(sig)), nil
}

// Verify parses token, checks its signature against pub, and returns the session ID
// it names, provided now is before the token's expiry. now is a parameter, rather
// than time.Now(), so callers and tests can check expiry deterministically.
func Verify(pub *ecdsa.PublicKey, token string, now time.Time) (sessionID string, err error) {
	// Session IDs are UUIDs and never contain a ".", but parse defensively: a
	// malformed token must fail cleanly, not panic or misparse.
	parts := strings.Split(token, ".")
	if len(parts) != 4 {
		return "", fmt.Errorf("sessiontoken: malformed token: expected 4 dot-separated parts, got %d", len(parts))
	}
	if parts[0] != version {
		return "", fmt.Errorf("sessiontoken: unsupported token version %q", parts[0])
	}
	sessionID = parts[1]
	expirySecs, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return "", fmt.Errorf("sessiontoken: malformed expiry: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		return "", fmt.Errorf("sessiontoken: malformed signature: %w", err)
	}
	d := digest(sessionID, expirySecs)
	if !ecdsa.VerifyASN1(pub, d[:], sig) {
		return "", fmt.Errorf("sessiontoken: signature verification failed")
	}
	if !now.Before(time.Unix(expirySecs, 0)) {
		return "", fmt.Errorf("sessiontoken: expired at %s", time.Unix(expirySecs, 0))
	}
	return sessionID, nil
}

// digest returns the SHA-256 digest Mint signs and Verify checks: the hash of
// "telepresence-session-token/v1|<sessionID>|<expiryUnixSeconds>".
func digest(sessionID string, expirySecs int64) [sha256.Size]byte {
	return sha256.Sum256([]byte(signingDomain + "|" + sessionID + "|" + strconv.FormatInt(expirySecs, 10)))
}

// PublicKeyFromCertPEM parses the first CERTIFICATE block in caPEM and returns its
// public key, which must be ECDSA -- the only key type the manager's QUIC CA
// generates.
func PublicKeyFromCertPEM(caPEM []byte) (*ecdsa.PublicKey, error) {
	block, _ := pem.Decode(caPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("sessiontoken: no CERTIFICATE block found in PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("sessiontoken: parse certificate: %w", err)
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("sessiontoken: certificate public key is %T, not ECDSA", cert.PublicKey)
	}
	return pub, nil
}
