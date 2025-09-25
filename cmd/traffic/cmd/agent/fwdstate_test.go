package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func TestGenerateMechanismDescription(t *testing.T) {
	tests := []struct {
		name     string
		spec     *manager.InterceptSpec
		expected string
	}{
		{
			name: "TCP mechanism",
			spec: &manager.InterceptSpec{
				Mechanism: "tcp",
			},
			expected: "all TCP connections",
		},
		{
			name: "HTTP mechanism with no filters",
			spec: &manager.InterceptSpec{
				Mechanism: "http",
			},
			expected: "all HTTP connections",
		},
		{
			name: "HTTP mechanism with header filters only",
			spec: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"X-User-ID":     "dev123",
					"X-Environment": "staging",
				},
			},
			expected: "HTTP filters: header X-User-ID=dev123, header X-Environment=staging",
		},
		{
			name: "HTTP mechanism with path filters only",
			spec: &manager.InterceptSpec{
				Mechanism: "http",
				PathFilters: []string{
					"/api/v1/*",
					"/admin/*",
				},
			},
			expected: "HTTP filters: path /api/v1/*, path /admin/*",
		},
		{
			name: "HTTP mechanism with both header and path filters",
			spec: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"X-User-ID": "dev123",
				},
				PathFilters: []string{
					"/api/*",
				},
			},
			expected: "HTTP filters: header X-User-ID=dev123, path /api/*",
		},
		{
			name: "HTTP mechanism with single header filter",
			spec: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"Authorization": "Bearer token123",
				},
			},
			expected: "HTTP filters: header Authorization=Bearer token123",
		},
		{
			name: "HTTP mechanism with single path filter",
			spec: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{"/health"},
			},
			expected: "HTTP filters: path /health",
		},
		{
			name: "unknown mechanism defaults to TCP",
			spec: &manager.InterceptSpec{
				Mechanism: "unknown",
			},
			expected: "all TCP connections",
		},
		{
			name: "empty mechanism defaults to TCP",
			spec: &manager.InterceptSpec{
				Mechanism: "",
			},
			expected: "all TCP connections",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generateMechanismDescription(tt.spec)
			assert.Equal(t, tt.expected, result)
		})
	}
}
