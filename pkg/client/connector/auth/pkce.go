package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

const (
	codeVerifierLength      = 32
	PKCEChallengeMethodS256 = "S256"
)

type CodeVerifier struct {
	Value string
}

func CreateCodeVerifier() (*CodeVerifier, error) {
	b := make([]byte, codeVerifierLength)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return &CodeVerifier{base64.RawURLEncoding.EncodeToString(b)}, nil
}

func (v *CodeVerifier) String() string {
	return v.Value
}

func (v *CodeVerifier) CodeChallengePlain() string {
	return v.Value
}

func (v *CodeVerifier) CodeChallengeS256() string {
	h := sha256.New()
	_, _ = h.Write([]byte(v.Value))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
