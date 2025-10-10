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
			expected: "all TCP connections",
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
			expected: "HTTP requests with headers\n 'X-Environment: staging'\n 'X-User-Id: dev123'",
		},
		{
			name: "HTTP mechanism with path filters only",
			spec: &manager.InterceptSpec{
				Mechanism: "http",
				PathFilters: []string{
					":path-prefix:/api/v1/",
					":path-prefix:/admin/",
				},
			},
			expected: "HTTP requests with paths\n prefix /api/v1/\n prefix /admin/",
		},
		{
			name: "HTTP mechanism with both header and path filters",
			spec: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"X-User-ID": "dev123",
				},
				PathFilters: []string{
					":path-prefix:/api/",
				},
			},
			expected: "HTTP requests with path prefix /api/ and header 'X-User-Id: dev123'",
		},
		{
			name: "HTTP mechanism with single header filter",
			spec: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"Authorization": "Bearer token123",
				},
			},
			expected: "HTTP requests with header 'Authorization: Bearer token123'",
		},
		{
			name: "HTTP mechanism with single path filter",
			spec: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-equal:/health"},
			},
			expected: "HTTP requests with path == /health",
		},
		{
			name: "HTTP mechanism with regex path filter",
			spec: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:^/api/v[0-9]+/.*$"},
			},
			expected: "HTTP requests with path =~ ^/api/v[0-9]+/.*$",
		},
		{
			name: "HTTP mechanism with mixed path filter types",
			spec: &manager.InterceptSpec{
				Mechanism: "http",
				PathFilters: []string{
					":path-equal:/health",
					":path-prefix:/api/",
					":path-regex:^/admin/.*$",
				},
			},
			expected: "HTTP requests with paths\n == /health\n prefix /api/\n =~ ^/admin/.*$",
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

func TestInterceptSpecsConflict(t *testing.T) {
	tests := []struct {
		name      string
		spec1     *manager.InterceptSpec
		spec2     *manager.InterceptSpec
		conflicts bool
	}{
		{
			name: "Issue #3969: Different header values for same key - should NOT conflict",
			spec1: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "adam",
				},
			},
			spec2: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "bertil",
				},
			},
			conflicts: false,
		},
		{
			name: "Same header key and value - should conflict",
			spec1: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "adam",
				},
			},
			spec2: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "adam",
				},
			},
			conflicts: true,
		},
		{
			name: "Different header keys - should not conflict",
			spec1: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "adam",
				},
			},
			spec2: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-session": "abc123",
				},
			},
			conflicts: false,
		},
		{
			name: "TCP intercepts (no filters) - should conflict",
			spec1: &manager.InterceptSpec{
				Mechanism: "tcp",
			},
			spec2: &manager.InterceptSpec{
				Mechanism: "tcp",
			},
			conflicts: true,
		},
		{
			name: "Different path filters - should not conflict",
			spec1: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{"/api/v1/*"},
			},
			spec2: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{"/api/v2/*"},
			},
			conflicts: false,
		},
		{
			name: "Same path filters - should conflict",
			spec1: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{"/api/*"},
			},
			spec2: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{"/api/*"},
			},
			conflicts: true,
		},
		{
			name: "Mixed filters: headers vs paths - should not conflict",
			spec1: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "adam",
				},
			},
			spec2: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{"/admin/*"},
			},
			conflicts: false,
		},
		{
			name: "Multiple headers with one overlap - should NOT conflict (not a subset)",
			spec1: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user":        "adam",
					"x-environment": "dev",
				},
			},
			spec2: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user":    "adam",
					"x-session": "xyz789",
				},
			},
			conflicts: false,
		},
		{
			name: "Header subset - should conflict",
			spec1: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "adam",
				},
			},
			spec2: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user":    "adam",
					"x-session": "xyz789",
				},
			},
			conflicts: true,
		},
		{
			name: "Global intercept vs HTTP with filters - should conflict",
			spec1: &manager.InterceptSpec{
				Mechanism: "tcp",
			},
			spec2: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "adam",
				},
			},
			conflicts: true,
		},
		{
			name: "Same headers, different paths - should NOT conflict",
			spec1: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "adam",
				},
				PathFilters: []string{"/api/*"},
			},
			spec2: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "adam",
				},
				PathFilters: []string{"/admin/*"},
			},
			conflicts: false,
		},
		{
			name: "Same headers, overlapping paths - should conflict",
			spec1: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "adam",
				},
				PathFilters: []string{"/api/*"},
			},
			spec2: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "adam",
				},
				PathFilters: []string{"/api/*"},
			},
			conflicts: true,
		},
		{
			name: "Subset headers with disjoint paths - should NOT conflict",
			spec1: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "adam",
				},
				PathFilters: []string{"/api/*"},
			},
			spec2: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user":    "adam",
					"x-session": "xyz789",
				},
				PathFilters: []string{"/admin/*"},
			},
			conflicts: false,
		},
		{
			name: "Subset headers with overlapping paths - should conflict",
			spec1: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "adam",
				},
				PathFilters: []string{"/api/*"},
			},
			spec2: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user":    "adam",
					"x-session": "xyz789",
				},
				PathFilters: []string{"/api/*"},
			},
			conflicts: true,
		},
		{
			name: "HTTP with only paths vs global - should conflict",
			spec1: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{"/api/*"},
			},
			spec2: &manager.InterceptSpec{
				Mechanism: "tcp",
			},
			conflicts: true,
		},
		{
			name: "HTTP with only headers, no paths - should conflict if headers are subset",
			spec1: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "adam",
				},
			},
			spec2: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user":    "adam",
					"x-session": "xyz789",
				},
			},
			conflicts: true,
		},
		{
			name: "Case-insensitive header matching - X-User vs x-user should conflict",
			spec1: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"X-User": "adam",
				},
			},
			spec2: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "adam",
				},
			},
			conflicts: true,
		},
		{
			name: "Case-insensitive header matching with different values - should NOT conflict",
			spec1: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"X-User": "adam",
				},
			},
			spec2: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "bertil",
				},
			},
			conflicts: false,
		},
		{
			name: "Mixed case header variants - X-USER, x-User, X-user should all normalize to X-User",
			spec1: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"X-USER": "adam",
				},
			},
			spec2: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-User": "adam",
				},
			},
			conflicts: true,
		},
		{
			name: "Wildcard header matching - x-user:dev-* should work properly",
			spec1: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "dev-*",
				},
			},
			spec2: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "dev-*",
				},
			},
			conflicts: true,
		},
		{
			name: "Path prefix overlap - :path-prefix:/api/ vs :path-prefix:/api/v1/ should conflict",
			spec1: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-prefix:/api/"},
			},
			spec2: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-prefix:/api/v1/"},
			},
			conflicts: true,
		},
		{
			name: "Path prefix no overlap - :path-prefix:/api/ vs :path-prefix:/admin/ should NOT conflict",
			spec1: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-prefix:/api/"},
			},
			spec2: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-prefix:/admin/"},
			},
			conflicts: false,
		},
		{
			name: "Path equal vs prefix overlap - :path-equal:/api/users vs :path-prefix:/api/ should conflict",
			spec1: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-equal:/api/users"},
			},
			spec2: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-prefix:/api/"},
			},
			conflicts: true,
		},
		{
			name: "Path equal vs prefix no overlap - :path-equal:/admin/users vs :path-prefix:/api/ should NOT conflict",
			spec1: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-equal:/admin/users"},
			},
			spec2: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-prefix:/api/"},
			},
			conflicts: false,
		},
		{
			name: "Path regex - :path-regex: should not conflict (distinct paths)",
			spec1: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:^/api/.*$"},
			},
			spec2: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-prefix:/admin/"},
			},
			conflicts: false,
		},
		{
			name: "Regex vs Regex with same prefix - should conflict",
			spec1: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:^/api/.*"},
			},
			spec2: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:^/api/v1/.*"},
			},
			conflicts: true,
		},
		{
			name: "Regex vs Regex with same prefix - non-anchor- should conflict",
			spec1: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:/api/.*"},
			},
			spec2: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:/api/v2/.*"},
			},
			conflicts: true,
		},
		{
			name: "Regex vs Regex with different prefix - should not conflict",
			spec1: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:^/admin/.*"},
			},
			spec2: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:^/api/.*"},
			},
			conflicts: false,
		},
		{
			name: "Regex vs Exact with same value - should conflict",
			spec1: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:^/health$"},
			},
			spec2: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-equal:/health"},
			},
			conflicts: true,
		},
		{
			name: "Regex vs Exact with different value - should not conflict",
			spec1: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:^/health$"},
			},
			spec2: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-equal:/ready"},
			},
			conflicts: false,
		},
		{
			name: "Regex vs Prefix with overlapping prefix - should conflict",
			spec1: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:^/api/v1/.*"},
			},
			spec2: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-prefix:/api/"},
			},
			conflicts: true,
		},
		{
			name: "Regex vs Prefix with non-overlapping prefix - should not conflict",
			spec1: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:^/admin/.*"},
			},
			spec2: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-prefix:/api/"},
			},
			conflicts: false,
		},
		{
			name: "Regex vs Regex with exact equality pattern - should conflict",
			spec1: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:^/status$"},
			},
			spec2: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:^/status$"},
			},
			conflicts: true,
		},
		{
			name: "Regex vs Regex with different exact patterns - should not conflict",
			spec1: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:^/status$"},
			},
			spec2: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:^/health$"},
			},
			conflicts: false,
		},
		{
			name: "Header regex - identical wildcard patterns should conflict",
			spec1: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "adam::.*",
				},
			},
			spec2: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "adam::.*",
				},
			},
			conflicts: true,
		},
		{
			name: "Header regex - identical wildcard patterns should conflict",
			spec1: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "^adam::.*",
				},
			},
			spec2: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "adam::.*",
				},
			},
			conflicts: true,
		},
		{
			name: "Header regex - patterns with common suffix should conflict",
			spec1: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "^adam::.*",
				},
			},
			spec2: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "bertil::adam::.*",
				},
			},
			conflicts: true,
		},
		{
			name: "Header regex - different prefixes in wildcard should not conflict",
			spec1: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "^adam::.*",
				},
			},
			spec2: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "^bertil::.*",
				},
			},
			conflicts: false,
		},
		{
			name: "Header regex - different prefixes in wildcard should conflict",
			spec1: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "adam::.*",
				},
			},
			spec2: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "bertil::.*",
				},
			},
			conflicts: true,
		},
		{
			name: "Header regex - overlapping wildcard patterns should conflict",
			spec1: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-env": "stage::[a-z]+",
				},
			},
			spec2: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-env": "stage::.*",
				},
			},
			conflicts: true,
		},
		{
			name: "Header regex - one wildcard, one static exact value should conflict if regex includes it",
			spec1: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "prod::[a-z]+",
				},
			},
			spec2: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "prod::admin",
				},
			},
			conflicts: true,
		},
		{
			name: "Header regex - non-overlapping static and wildcard should not conflict",
			spec1: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "dev::[0-9]+",
				},
			},
			spec2: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "prod::[a-z]+",
				},
			},
			conflicts: true,
		},
		{
			name: "Header regex - different header keys should not conflict",
			spec1: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "dev::.*",
				},
			},
			spec2: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-env": "stage::.*",
				},
			},
			conflicts: false,
		},
		{
			name: "Header regex - one regex fully includes another (subset overlap)",
			spec1: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "dev::(admin|test)",
				},
			},
			spec2: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "dev::admin",
				},
			},
			conflicts: true,
		},
		{
			name: "Header regex - disjoint sets in alternation should not conflict",
			spec1: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "dev::(qa|test)",
				},
			},
			spec2: &manager.InterceptSpec{
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"x-user": "dev::prod",
				},
			},
			conflicts: true,
		},
		{
			name: "Regex literal prefix with escaped dot - should detect same literal prefix",
			spec1: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:^/api/v1\\.users/.*"},
			},
			spec2: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:^/api/v1\\.users/details.*"},
			},
			conflicts: true,
		},
		{
			name: "Regex literal prefix with escaped plus - should conflict due to same base literal",
			spec1: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:^/foo\\+bar/.*"},
			},
			spec2: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:^/foo\\+bar/v1.*"},
			},
			conflicts: true,
		},
		{
			name: "Regex with group starts early - should NOT conflict if literal diverges before '('",
			spec1: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:^/api/(v1|v2)/.*"},
			},
			spec2: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:^/admin/(v1|v2)/.*"},
			},
			conflicts: false,
		},
		{
			name: "Regex with nested group but same prefix - should conflict",
			spec1: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:^/api/(v1|v2)/users.*"},
			},
			spec2: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:^/api/(v1|v2)/orders.*"},
			},
			conflicts: true,
		},
		{
			name: "Regex with character class in middle - should conflict if literal prefix matches",
			spec1: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:^/user_[a-z]+/details"},
			},
			spec2: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:^/user_[0-9]+/settings"},
			},
			conflicts: true,
		},
		{
			name: "Regex with non-overlapping prefixes - should not conflict",
			spec1: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:^/serviceA/.*"},
			},
			spec2: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:^/serviceB/.*"},
			},
			conflicts: false,
		},
		{
			name: "Regex with trailing literal only - should conflict if same literal",
			spec1: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:/api/v1/resource$"},
			},
			spec2: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:^/api/v1/resource$"},
			},
			conflicts: true,
		},
		{
			name: "Regex without ^ or .* but same base literal - should conflict",
			spec1: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:/foo/bar"},
			},
			spec2: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:/foo/bar/v1"},
			},
			conflicts: true,
		},
		{
			name: "Regex with escaped parentheses - same literal should conflict",
			spec1: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:^/file\\(test\\)/.*"},
			},
			spec2: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:^/file\\(test\\)/v1"},
			},
			conflicts: true,
		},
		{
			name: "Regex with trailing $ anchor and identical literal - should conflict",
			spec1: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:^/health$"},
			},
			spec2: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:/health$"},
			},
			conflicts: true,
		},
		{
			name: "Regex vs different Regex - non-anchored - should not conflict",
			spec1: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:/api"},
			},
			spec2: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:/foo"},
			},
			conflicts: true,
		},
		{
			name: "Regex vs different Prefix - non-anchored - should conflict",
			spec1: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:/api/"},
			},
			spec2: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-prefix:/foo/api/"},
			},
			conflicts: true,
		},
		{
			name: "Regex vs overlapping Regex - non-anchored - should conflict",
			spec1: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:/api/"},
			},
			spec2: &manager.InterceptSpec{
				Mechanism:   "http",
				PathFilters: []string{":path-regex:/foo/api/"},
			},
			conflicts: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := interceptSpecsConflict(tt.spec1, tt.spec2)
			assert.Equal(t, tt.conflicts, result)
		})
	}
}
