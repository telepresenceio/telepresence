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

// kafkaKeySuffix marks a spec whose Kafka provider was forced on by
// WithKafka, so its Key (and Hash) never collides with the same spec
// without the provider.
const kafkaKeySuffix = "+kafka"

// WithKafka returns a copy of s with the Kafka provider forced on
// (kafka.enabled=true) and its Key suffixed, so a manager release installed
// with the provider is never adopted for, or by, a run without it. A no-op
// if s already enables Kafka (e.g. the Kafka() catalog entry itself).
func (s Spec) WithKafka() Spec {
	if s.Values.Kafka.Enabled {
		return s
	}
	values := s.Values
	values.Kafka = KafkaValues{Enabled: true}
	return Spec{Key: s.Key + kafkaKeySuffix, Values: values}
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
