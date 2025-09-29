package matcher

import (
	"net/http"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewRequest(t *testing.T) {
	tests := []struct {
		name string
		args map[string]string
		want Request
	}{
		{
			name: "empty",
			args: nil,
			want: &request{},
		},
		{
			name: "path-equal",
			args: map[string]string{":paths:": ":path-equal:/some/path"},
			want: &request{paths: Paths{NewEqual("/some/path")}},
		},
		{
			name: "path-prefix",
			args: map[string]string{":paths:": ":path-prefix:/some/path"},
			want: &request{paths: Paths{NewPrefix("/some/path")}},
		},
		{
			name: "path-regex",
			args: map[string]string{":paths:": ":path-regex:.*/path"},
			want: &request{paths: Paths{rxValue{regexp.MustCompile(".*/path")}}},
		},
		{
			name: "path-regex and headers",
			args: map[string]string{":paths:": ":path-regex:.*/path", "A": "b"},
			want: &request{paths: Paths{rxValue{regexp.MustCompile(".*/path")}}, headers: HeaderMap(map[string]Value{"A": NewEqual("b")})},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewRequestFromMap(tt.args)
			assert.Equalf(t, tt.want, got, "NewRequest(%v)", tt.args)
		})
	}
}

func Test_request_Map(t *testing.T) {
	tests := []struct {
		name    string
		request request
		want    map[string]string
	}{
		{
			"empty",
			request{},
			nil,
		},
		{
			"path-equal",
			request{paths: Paths{NewEqual("/some/path")}},
			map[string]string{":paths:": ":path-equal:/some/path"},
		},
		{
			"path-prefix",
			request{paths: Paths{NewPrefix("/some/path")}},
			map[string]string{":paths:": ":path-prefix:/some/path"},
		},
		{
			"path-regex",
			request{paths: Paths{rxValue{regexp.MustCompile(".*/path")}}},
			map[string]string{":paths:": ":path-regex:.*/path"},
		},
		{
			"path-regex and headers",
			request{paths: Paths{rxValue{regexp.MustCompile(".*/path")}}, headers: HeaderMap(map[string]Value{"A": NewEqual("b")})},
			map[string]string{":paths:": ":path-regex:.*/path", "A": "b"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, tt.request.Map(), "Map()")
		})
	}
}

func Test_request_Matches(t *testing.T) {
	tests := []struct {
		name    string
		request request
		path    string
		headers http.Header
		want    bool
	}{
		{
			name:    "empty",
			request: request{},
			path:    "/some/path",
			headers: http.Header(map[string][]string{"A": {"b"}}),
			want:    true,
		},
		{
			name:    "path and headers",
			request: request{paths: Paths{rxValue{regexp.MustCompile(".*/path")}}, headers: HeaderMap(map[string]Value{"A": NewEqual("b")})},
			path:    "/some/path",
			headers: http.Header(map[string][]string{"A": {"b"}}),
			want:    true,
		},
		{
			name:    "path and headers mismatch on just path",
			request: request{paths: Paths{rxValue{regexp.MustCompile(".*/path")}}, headers: HeaderMap(map[string]Value{"A": NewEqual("b")})},
			path:    "/some/path",
			headers: nil,
			want:    false,
		},
		{
			name:    "path and headers mismatch on just headers",
			request: request{paths: Paths{rxValue{regexp.MustCompile(".*/path")}}, headers: HeaderMap(map[string]Value{"A": NewEqual("b")})},
			path:    "",
			headers: http.Header(map[string][]string{"A": {"b"}}),
			want:    false,
		},
		{
			name:    "path-equal",
			request: request{paths: Paths{NewEqual("/some/path")}},
			path:    "/some/path",
			want:    true,
		},
		{
			name:    "path-equal mismatch",
			request: request{paths: Paths{NewEqual("/some")}},
			path:    "/some/path",
			want:    false,
		},
		{
			name:    "path-prefix",
			request: request{paths: Paths{NewPrefix("/some")}},
			path:    "/some/path",
			want:    true,
		},
		{
			name:    "path-prefix mismatch",
			request: request{paths: Paths{NewPrefix("/some")}},
			path:    "/other/path",
			want:    false,
		},
		{
			name:    "path-regex",
			request: request{paths: Paths{rxValue{regexp.MustCompile(".*/path")}}},
			path:    "/some/path",
			want:    true,
		},
		{
			name:    "path-regex mismatch",
			request: request{paths: Paths{rxValue{regexp.MustCompile(".*/path")}}},
			path:    "/some/road",
			want:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, tt.request.MatchesPathAndHeader(tt.path, tt.headers), "Matches(%v, %v)", tt.path, tt.headers)
		})
	}
}

func Test_request_String(t *testing.T) {
	tests := []struct {
		name    string
		request request
		want    string
	}{
		{
			name: "empty",
			want: "all TCP connections",
		},
		{
			name:    "path-equal",
			request: request{paths: Paths{NewEqual("/some/path")}},
			want:    "HTTP requests with path == /some/path",
		},
		{
			name:    "path-prefix",
			request: request{paths: Paths{NewPrefix("/some/path")}},
			want:    "HTTP requests with path prefix /some/path",
		},
		{
			name:    "path-regex",
			request: request{paths: Paths{rxValue{regexp.MustCompile(".*/path")}}},
			want:    "HTTP requests with path =~ .*/path",
		},
		{
			name:    "headers",
			request: request{headers: HeaderMap(map[string]Value{"A": NewEqual("b")})},
			want:    "HTTP requests with header 'A: b'",
		},
		{
			name:    "path and header",
			request: request{paths: Paths{rxValue{regexp.MustCompile(".*/path")}}, headers: HeaderMap(map[string]Value{"A": NewEqual("b")})},
			want:    "HTTP requests with path =~ .*/path and header 'A: b'",
		},
		{
			name:    "path and headers",
			request: request{paths: Paths{rxValue{regexp.MustCompile(".*/path")}}, headers: HeaderMap(map[string]Value{"A": NewEqual("b"), "B": NewEqual("c")})},
			want:    "HTTP requests with\n path =~ .*/path\n headers\n  'A: b'\n  'B: c'",
		},
		{
			name:    "paths and headers",
			request: request{paths: Paths{NewPrefix("/some/path"), rxValue{regexp.MustCompile(".*/path")}}, headers: HeaderMap(map[string]Value{"A": NewEqual("b"), "B": NewEqual("c")})},
			want:    "HTTP requests with\n paths\n  prefix /some/path\n  =~ .*/path\n headers\n  'A: b'\n  'B: c'",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, tt.request.String(), "String()")
		})
	}
}
