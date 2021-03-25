package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"math/rand"
	"time"
)

const (
	codeVerifierLength      = 32
	PKCEChallengeMethodS256 = "S256"
)

type CodeVerifier struct {
	Value string
}

func CreateCodeVerifier() *CodeVerifier {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	b := make([]byte, codeVerifierLength)
	for i := 0; i < codeVerifierLength; i++ {
		b[i] = byte(r.Intn(255))
	}
	return &CodeVerifier{base64.RawURLEncoding.EncodeToString(b)}
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
