package header

import (
	"net/http"
	"testing"
)

func Test_headerMatcher_Matches(t *testing.T) {
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
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			hm := NewMatcher(tt.match)
			if got := hm.Matches(tt.header); got != tt.want {
				t.Errorf("Matches() = %v, want %v", got, tt.want)
			}
		})
	}
}
