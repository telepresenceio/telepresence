package licensetest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"time"

	"gopkg.in/square/go-jose.v2"
	"gopkg.in/square/go-jose.v2/jwt"
)

// GenKeys generates a private key to be used for creating a
// jwt token for testing.
func GenKeys() (*rsa.PrivateKey, string, error) {
	reader := rand.Reader
	bitSize := 2048
	privateKey, err := rsa.GenerateKey(reader, bitSize)
	if err != nil {
		return nil, "", err
	}

	pkBytes, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return nil, "", err
	}
	pem := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PUBLIC KEY",
		Bytes: pkBytes,
	})
	return privateKey, string(pem), nil
}

// GenLincese generates a new license with clusterID as its audience, with claims as its claims, and is signed by key.
// Claims audience and expiry are automatically set but will not override what is in passed in claims.
func GenLicense(key *rsa.PrivateKey, clusterID string, claims map[string]interface{}) (string, error) {
	signer, err := jose.NewSigner(jose.SigningKey{
		Algorithm: jose.RS256,
		Key:       key,
	}, nil)
	if err != nil {
		return "", err
	}

	if claims == nil {
		claims = make(map[string]interface{})
	}
	if _, exists := claims["aud"]; !exists {
		claims["aud"] = []string{clusterID}
	}
	if _, exists := claims["exp"]; !exists {
		// Generate expiration date
		timeNow := time.Now()
		dayOffset := 1
		expTime := timeNow.AddDate(0, 0, dayOffset)
		expTimeDate := jwt.NewNumericDate(expTime)
		claims["exp"] = expTimeDate
	}

	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signedPayload, err := signer.Sign(payload)
	if err != nil {
		return "", err
	}
	license, err := signedPayload.CompactSerialize()
	if err != nil {
		return "", err
	}
	return license, nil
}
