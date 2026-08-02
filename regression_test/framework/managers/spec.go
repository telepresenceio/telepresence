// Package managers defines named traffic-manager helm value sets used by
// suites to declare the manager configuration they need.
package managers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
)

// Spec identifies a traffic-manager configuration: a catalog key plus the
// values overlay applied on top of Baseline.
type Spec struct {
	Key    string
	Values Values
}

// specHash is the JSON shape hashed by Hash. Field order is fixed by the
// struct declaration, and json.Marshal sorts map keys, so the output is
// canonical.
type specHash struct {
	Key    string `json:"key"`
	Values Values `json:"values"`
}

// Hash returns the sha256, hex-encoded identity of the spec: a canonical JSON
// encoding of the Key and Values. Two specs with the same Hash produce the
// same helm release and can share it.
func (s Spec) Hash() string {
	data, err := json.Marshal(specHash(s))
	if err != nil {
		// Values is a plain data struct; a marshal failure is a programming
		// error (e.g. a field that can't be encoded), not a runtime condition.
		panic(fmt.Sprintf("managers: hashing spec %q: %v", s.Key, err))
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
