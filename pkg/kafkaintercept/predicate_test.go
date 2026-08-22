package kafkaintercept

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/twmb/franz-go/pkg/kgo"
)

func TestPredicateMatchesLastHeader(t *testing.T) {
	record := &kgo.Record{
		Key: []byte("customer-17"),
		Headers: []kgo.RecordHeader{
			{Key: "tenant", Value: []byte("old")},
			{Key: "tenant", Value: []byte("blue")},
		},
	}
	require.True(t, Predicate{
		Headers:   map[string][]byte{"tenant": []byte("blue")},
		KeyPrefix: []byte("customer-"),
	}.Matches(record))
	assert.False(t, Predicate{Headers: map[string][]byte{"tenant": []byte("old")}}.Matches(record))
}

func TestPredicateOverlap(t *testing.T) {
	tests := []struct {
		name string
		a    Predicate
		b    Predicate
		want bool
	}{
		{
			name: "different value of same header",
			a:    Predicate{Headers: map[string][]byte{"tenant": []byte("blue")}},
			b:    Predicate{Headers: map[string][]byte{"tenant": []byte("green")}},
		},
		{
			name: "different headers can coexist on one record",
			a:    Predicate{Headers: map[string][]byte{"tenant": []byte("blue")}},
			b:    Predicate{Headers: map[string][]byte{"region": []byte("west")}},
			want: true,
		},
		{
			name: "disjoint exact keys",
			a:    Predicate{Key: []byte("one")},
			b:    Predicate{Key: []byte("two")},
		},
		{
			name: "exact key under prefix",
			a:    Predicate{Key: []byte("customer-17")},
			b:    Predicate{KeyPrefix: []byte("customer-")},
			want: true,
		},
		{
			name: "disjoint prefixes",
			a:    Predicate{KeyPrefix: []byte("customer-")},
			b:    Predicate{KeyPrefix: []byte("order-")},
		},
		{
			name: "unfiltered overlaps",
			a:    Predicate{},
			b:    Predicate{Key: []byte("one")},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.a.Overlaps(tt.b))
			assert.Equal(t, tt.want, tt.b.Overlaps(tt.a))
		})
	}
}

func TestPredicateValidation(t *testing.T) {
	require.Error(t, Predicate{Key: []byte("x"), KeyPrefix: []byte("x")}.Validate())
	require.Error(t, Predicate{Headers: map[string][]byte{"": []byte("x")}}.Validate())
	require.NoError(t, Predicate{Headers: map[string][]byte{"tenant": nil}}.Validate())
}
