package userd_auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

const (
	codeVerifierLength      = 32
	PKCEChallengeMethodS256 = "S256"
)

type CodeVerifier string

func NewCodeVerifier() (CodeVerifier, error) {
	b := make([]byte, codeVerifierLength)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return CodeVerifier(base64.RawURLEncoding.EncodeToString(b)), nil
}

func (v CodeVerifier) String() string {
	return string(v)
}

func (v CodeVerifier) CodeChallengePlain() string {
	return string(v)
}

func (v CodeVerifier) CodeChallengeS256() string {
	h := sha256.New()
	_, _ = h.Write([]byte(v))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
