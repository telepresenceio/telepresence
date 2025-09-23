package forwarder

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHTTPInterceptor_shouldInterceptRequest(t *testing.T) {
	h := &httpInterceptor{
		headerFilters: map[string]string{
			"X-User-ID":     "dev123",
			"X-Environment": "staging",
		},
		pathFilters: []string{"/api/v1/*", "/admin/*"},
	}

	tests := []struct {
		name            string
		headers         map[string]string
		path            string
		shouldIntercept bool
	}{
		{
			name: "matching headers and path",
			headers: map[string]string{
				"X-User-ID":     "dev123",
				"X-Environment": "staging",
			},
			path:            "/api/v1/users",
			shouldIntercept: true,
		},
		{
			name: "matching headers but no path filters",
			headers: map[string]string{
				"X-User-ID":     "dev123",
				"X-Environment": "staging",
			},
			path:            "/some/other/path",
			shouldIntercept: false,
		},
		{
			name: "missing required header",
			headers: map[string]string{
				"X-User-ID": "dev123",
				// Missing X-Environment
			},
			path:            "/api/v1/users",
			shouldIntercept: false,
		},
		{
			name: "wrong header value",
			headers: map[string]string{
				"X-User-ID":     "prod456",
				"X-Environment": "staging",
			},
			path:            "/api/v1/users",
			shouldIntercept: false,
		},
		{
			name: "wildcard header match",
			headers: map[string]string{
				"X-User-ID":     "dev456",
				"X-Environment": "staging",
			},
			path:            "/api/v1/users",
			shouldIntercept: false, // Exact match required by default
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, "http://example.com"+tt.path, nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			result := h.shouldInterceptRequest(req.Context(), req, h.headerFilters, h.pathFilters)
			assert.Equal(t, tt.shouldIntercept, result)
		})
	}
}

func TestHTTPInterceptor_matchesPattern(t *testing.T) {
	h := &httpInterceptor{}

	tests := []struct {
		name    string
		value   string
		pattern string
		matches bool
	}{
		{"exact match", "dev123", "dev123", true},
		{"no match", "dev123", "prod456", false},
		{"wildcard match", "dev123", "dev*", true},
		{"wildcard no match", "prod123", "dev*", false},
		{"complex wildcard", "dev-user-123", "dev-*-123", true},
		{"empty value", "", "dev*", false},
		{"empty pattern", "dev123", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := h.matchesPattern(tt.value, tt.pattern)
			assert.Equal(t, tt.matches, result)
		})
	}
}

func TestHTTPInterceptor_noFilters(t *testing.T) {
	h := &httpInterceptor{
		headerFilters: map[string]string{},
		pathFilters:   []string{},
	}

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/any/path", nil)

	// No filters means intercept everything
	result := h.shouldInterceptRequest(req.Context(), req, h.headerFilters, h.pathFilters)
	assert.True(t, result)
}
