package matcher

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_headers_Matches(t *testing.T) {
	header := func(hm map[string]string) http.Header {
		hd := make(http.Header, len(hm))
		for k, v := range hm {
			hd.Set(k, v)
		}
		return hd
	}

	tests := []struct {
		name   string
		match  map[string]string
		header http.Header
		want   bool
	}{
		{
			name:   "exact match",
			match:  map[string]string{"x-some-header": "some value"},
			header: header(map[string]string{"x-some-header": "some value"}),
			want:   true,
		},
		{
			name:   "canonical name match",
			match:  map[string]string{"X-Some-Header": "some value"},
			header: header(map[string]string{"x-some-header": "some value"}),
			want:   true,
		},
		{
			name:   "all-caps name match",
			match:  map[string]string{"X-SOME-HEADER": "some value"},
			header: header(map[string]string{"x-some-header": "some value"}),
			want:   true,
		},
		{
			name:   "case sensitive value mismatch",
			match:  map[string]string{"x-some-header": "Some Value"},
			header: header(map[string]string{"x-some-header": "some value"}),
			want:   false,
		},
		{
			name:   "regexp match",
			match:  map[string]string{"x-some-header": ".*value"},
			header: header(map[string]string{"x-some-header": "some value"}),
			want:   true,
		},
		{
			name:   "regexp mismatch",
			match:  map[string]string{"x-some-header": ".*values"},
			header: header(map[string]string{"x-some-header": "some value"}),
			want:   false,
		},
		{
			name:   "superfluous headers match",
			match:  map[string]string{"a": "1"},
			header: header(map[string]string{"a": "1", "b": "2"}),
			want:   true,
		},
		{
			name:   "missing headers mismatch",
			match:  map[string]string{"a": "1", "b": "2"},
			header: header(map[string]string{"a": "1"}),
			want:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hm := NewHeaders(tt.match)
			assert.Equal(t, tt.want, hm.Matches(tt.header))
		})
	}
}

func Test_NewHeaders_error(t *testing.T) {
	m := NewHeaders(map[string]string{"a": "un(balanced"})
	v, ok := m.HeaderMap()["A"]
	require.True(t, ok)
	assert.Equal(t, v.Op(), ValueOpEqual)
	assert.Equal(t, v.String(), "un(balanced")
}
