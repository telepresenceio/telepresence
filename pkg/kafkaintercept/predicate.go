// Package kafkaintercept contains the Kafka-specific data plane used by
// personal intercepts.
package kafkaintercept

import (
	"bytes"
	"fmt"

	"github.com/twmb/franz-go/pkg/kgo"
)

// Predicate is an AND expression over the last value of named headers and an
// optional exact or prefix key.
type Predicate struct {
	Headers   map[string][]byte
	Key       []byte
	KeyPrefix []byte
}

// Validate checks that the predicate has one unambiguous key operation.
func (p Predicate) Validate() error {
	if p.Key != nil && p.KeyPrefix != nil {
		return fmt.Errorf("kafka predicate cannot contain both an exact key and a key prefix")
	}
	for name := range p.Headers {
		if name == "" {
			return fmt.Errorf("kafka predicate header name must not be empty")
		}
	}
	return nil
}

// Matches reports whether record satisfies the predicate.
func (p Predicate) Matches(record *kgo.Record) bool {
	switch {
	case p.Key != nil && !bytes.Equal(record.Key, p.Key):
		return false
	case p.KeyPrefix != nil && !bytes.HasPrefix(record.Key, p.KeyPrefix):
		return false
	}
	for name, value := range p.Headers {
		actual, ok := lastHeader(record.Headers, name)
		if !ok || !bytes.Equal(actual, value) {
			return false
		}
	}
	return true
}

// Overlaps reports whether one Kafka record can satisfy both predicates.
func (p Predicate) Overlaps(other Predicate) bool {
	for name, value := range p.Headers {
		if otherValue, ok := other.Headers[name]; ok && !bytes.Equal(value, otherValue) {
			return false
		}
	}
	return keysOverlap(p, other)
}

func keysOverlap(a, b Predicate) bool {
	switch {
	case a.Key != nil && b.Key != nil:
		return bytes.Equal(a.Key, b.Key)
	case a.Key != nil && b.KeyPrefix != nil:
		return bytes.HasPrefix(a.Key, b.KeyPrefix)
	case a.KeyPrefix != nil && b.Key != nil:
		return bytes.HasPrefix(b.Key, a.KeyPrefix)
	case a.KeyPrefix != nil && b.KeyPrefix != nil:
		return bytes.HasPrefix(a.KeyPrefix, b.KeyPrefix) || bytes.HasPrefix(b.KeyPrefix, a.KeyPrefix)
	default:
		return true
	}
}

func lastHeader(headers []kgo.RecordHeader, name string) ([]byte, bool) {
	for i := len(headers) - 1; i >= 0; i-- {
		if headers[i].Key == name {
			return headers[i].Value, true
		}
	}
	return nil, false
}
