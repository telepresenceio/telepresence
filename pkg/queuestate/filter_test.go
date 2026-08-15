package queuestate

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOverlapsDisjointOnSharedKey(t *testing.T) {
	a := map[string]string{"user": "thomas", "region": "eu"}
	b := map[string]string{"user": "alice", "region": "eu"}
	assert.False(t, Overlaps(a, b))
	assert.False(t, Overlaps(b, a))
}

func TestOverlapsSameSharedValues(t *testing.T) {
	a := map[string]string{"user": "thomas", "region": "eu"}
	b := map[string]string{"user": "thomas", "region": "eu", "tier": "gold"}
	assert.True(t, Overlaps(a, b))
	assert.True(t, Overlaps(b, a))
}

func TestOverlapsNoSharedKeys(t *testing.T) {
	a := map[string]string{"user": "thomas"}
	b := map[string]string{"region": "eu"}
	assert.True(t, Overlaps(a, b))
	assert.True(t, Overlaps(b, a))
}

func TestOverlapsEmptyVsNonEmpty(t *testing.T) {
	a := map[string]string{}
	b := map[string]string{"user": "thomas"}
	assert.True(t, Overlaps(a, b))
	assert.True(t, Overlaps(b, a))
}

func TestOverlapsBothEmpty(t *testing.T) {
	assert.True(t, Overlaps(map[string]string{}, map[string]string{}))
}

func TestOverlapsNilMaps(t *testing.T) {
	assert.True(t, Overlaps(nil, nil))
	assert.True(t, Overlaps(nil, map[string]string{"user": "thomas"}))
}

func TestOverlapsSymmetric(t *testing.T) {
	cases := []struct {
		name string
		a, b map[string]string
	}{
		{"disjoint on shared key", map[string]string{"user": "thomas"}, map[string]string{"user": "alice"}},
		{"same shared values", map[string]string{"user": "thomas"}, map[string]string{"user": "thomas", "region": "eu"}},
		{"no shared keys", map[string]string{"user": "thomas"}, map[string]string{"region": "eu"}},
		{"empty vs non-empty", map[string]string{}, map[string]string{"user": "thomas"}},
		{"both empty", map[string]string{}, map[string]string{}},
		{
			"multi-key partial overlap",
			map[string]string{"user": "thomas", "region": "eu", "tier": "gold"},
			map[string]string{"user": "thomas", "region": "us"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, Overlaps(c.a, c.b), Overlaps(c.b, c.a))
		})
	}
}
