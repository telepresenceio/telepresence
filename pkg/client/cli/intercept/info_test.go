package intercept

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func TestInfo_HTTPFilterDisplay(t *testing.T) {
	tests := []struct {
		name            string
		spec            *manager.InterceptSpec
		interceptInfo   *manager.InterceptInfo
		expectedPattern string
		description     string
	}{
		{
			name: "TCP intercept does not show filter section",
			spec: &manager.InterceptSpec{
				Name:      "test-tcp",
				Mechanism: "tcp",
			},
			interceptInfo: &manager.InterceptInfo{
				Id:                "test-id",
				Disposition:       manager.InterceptDispositionType_ACTIVE,
				PodIp:             "10.0.0.1",
				MechanismArgsDesc: "all TCP connections",
			},
			expectedPattern: "Intercept name",
			description:     "TCP intercepts should not show HTTP filter section",
		},
		{
			name: "HTTP intercept with header filters",
			spec: &manager.InterceptSpec{
				Name:      "test-http",
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"X-User-ID":     "dev123",
					"X-Environment": "staging",
				},
			},
			interceptInfo: &manager.InterceptInfo{
				Id:          "test-id",
				Disposition: manager.InterceptDispositionType_ACTIVE,
				PodIp:       "10.0.0.1",
			},
			expectedPattern: "HTTP filters: header X-User-ID=dev123, header X-Environment=staging",
			description:     "HTTP intercepts should show header filters",
		},
		{
			name: "HTTP intercept with path filters",
			spec: &manager.InterceptSpec{
				Name:      "test-http",
				Mechanism: "http",
				PathFilters: []string{
					"/api/v1/*",
					"/admin/*",
				},
			},
			interceptInfo: &manager.InterceptInfo{
				Id:          "test-id",
				Disposition: manager.InterceptDispositionType_ACTIVE,
				PodIp:       "10.0.0.1",
			},
			expectedPattern: "HTTP filters: path /api/v1/*, path /admin/*",
			description:     "HTTP intercepts should show path filters",
		},
		{
			name: "HTTP intercept with both header and path filters",
			spec: &manager.InterceptSpec{
				Name:      "test-http",
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"X-User-ID": "dev123",
				},
				PathFilters: []string{
					"/api/*",
				},
			},
			interceptInfo: &manager.InterceptInfo{
				Id:          "test-id",
				Disposition: manager.InterceptDispositionType_ACTIVE,
				PodIp:       "10.0.0.1",
			},
			expectedPattern: "HTTP filters: header X-User-ID=dev123, path /api/*",
			description:     "HTTP intercepts should show both header and path filters",
		},
		{
			name: "HTTP intercept with no filters",
			spec: &manager.InterceptSpec{
				Name:      "test-http",
				Mechanism: "http",
			},
			interceptInfo: &manager.InterceptInfo{
				Id:          "test-id",
				Disposition: manager.InterceptDispositionType_ACTIVE,
				PodIp:       "10.0.0.1",
			},
			expectedPattern: "all HTTP connections",
			description:     "HTTP intercepts with no filters should show 'all HTTP connections'",
		},
		{
			name: "HTTP intercept with FilterDesc override",
			spec: &manager.InterceptSpec{
				Name:      "test-http",
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"X-User-ID": "dev123",
				},
			},
			interceptInfo: &manager.InterceptInfo{
				Id:                "test-id",
				Disposition:       manager.InterceptDispositionType_ACTIVE,
				PodIp:             "10.0.0.1",
				MechanismArgsDesc: "custom description",
			},
			expectedPattern: "custom description",
			description:     "FilterDesc should override generated HTTP filter description",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up minimal required fields
			tt.spec.TargetHost = "127.0.0.1"
			tt.spec.TargetPort = 8080
			tt.spec.Protocol = string(v1.ProtocolTCP)
			tt.interceptInfo.Spec = tt.spec

			// Create Info object
			info := NewInfo(context.Background(), tt.interceptInfo, false, nil)

			// Get the string representation
			output := info.String()

			// Check if the expected pattern is in the output
			assert.Contains(t, output, tt.expectedPattern, tt.description)

			// Verify that TCP intercepts don't show filter info (Global should be true)
			if tt.spec.Mechanism == "tcp" {
				assert.True(t, info.Global, "TCP intercepts should have Global=true")
				// Should NOT contain "HTTP filters:" for TCP intercepts
				assert.NotContains(t, output, "HTTP filters:", "TCP intercepts should not show HTTP filter info")
			} else {
				assert.False(t, info.Global, "HTTP intercepts should have Global=false")
			}
		})
	}
}

func TestNewInfo_HTTPFilterPopulation(t *testing.T) {
	tests := []struct {
		name                string
		spec                *manager.InterceptSpec
		expectedHeaderCount int
		expectedPathCount   int
		expectedGlobal      bool
	}{
		{
			name: "TCP intercept",
			spec: &manager.InterceptSpec{
				Name:      "test-tcp",
				Mechanism: "tcp",
			},
			expectedHeaderCount: 0,
			expectedPathCount:   0,
			expectedGlobal:      true,
		},
		{
			name: "HTTP intercept with filters",
			spec: &manager.InterceptSpec{
				Name:      "test-http",
				Mechanism: "http",
				HeaderFilters: map[string]string{
					"X-User-ID":     "dev123",
					"X-Environment": "staging",
				},
				PathFilters: []string{
					"/api/v1/*",
					"/admin/*",
				},
			},
			expectedHeaderCount: 2,
			expectedPathCount:   2,
			expectedGlobal:      false,
		},
		{
			name: "HTTP intercept with no filters",
			spec: &manager.InterceptSpec{
				Name:      "test-http",
				Mechanism: "http",
			},
			expectedHeaderCount: 0,
			expectedPathCount:   0,
			expectedGlobal:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up minimal required fields
			tt.spec.TargetHost = "127.0.0.1"
			tt.spec.TargetPort = 8080
			tt.spec.Protocol = string(v1.ProtocolTCP)

			interceptInfo := &manager.InterceptInfo{
				Id:          "test-id",
				Disposition: manager.InterceptDispositionType_ACTIVE,
				PodIp:       "10.0.0.1",
				Spec:        tt.spec,
			}

			// Create Info object
			info := NewInfo(context.Background(), interceptInfo, false, nil)

			// Verify field population
			assert.Len(t, info.HeaderFilters, tt.expectedHeaderCount, "Header filters should be populated correctly")
			assert.Len(t, info.PathFilters, tt.expectedPathCount, "Path filters should be populated correctly")
			assert.Equal(t, tt.expectedGlobal, info.Global, "Global flag should be set correctly")

			// Verify header filter content
			if len(tt.spec.HeaderFilters) > 0 {
				for key, value := range tt.spec.HeaderFilters {
					assert.Equal(t, value, info.HeaderFilters[key], "Header filter values should match")
				}
			}

			// Verify path filter content
			if len(tt.spec.PathFilters) > 0 {
				assert.Equal(t, tt.spec.PathFilters, info.PathFilters, "Path filters should match")
			}
		})
	}
}

func TestInfo_WriteTo_HTTPFilterFormatting(t *testing.T) {
	// Test specific formatting of HTTP filters in WriteTo output
	spec := &manager.InterceptSpec{
		Name:       "test-service",
		Mechanism:  "http",
		TargetHost: "127.0.0.1",
		TargetPort: 8080,
		Protocol:   string(v1.ProtocolTCP),
		HeaderFilters: map[string]string{
			"X-User-ID":     "dev123",
			"Authorization": "Bearer token=abc123",
		},
		PathFilters: []string{
			"/api/v1/*",
			"/health",
		},
	}

	interceptInfo := &manager.InterceptInfo{
		Id:          "test-id",
		Disposition: manager.InterceptDispositionType_ACTIVE,
		PodIp:       "10.0.0.1",
		Spec:        spec,
	}

	info := NewInfo(context.Background(), interceptInfo, false, nil)
	output := info.String()

	// Should contain HTTP filters section
	assert.Contains(t, output, "HTTP filters:", "Output should contain HTTP filters label")

	// Should contain all header filters (order may vary due to map iteration)
	assert.Contains(t, output, "header X-User-ID=dev123", "Output should contain X-User-ID header filter")
	assert.Contains(t, output, "header Authorization=Bearer token=abc123", "Output should contain Authorization header filter with equals in value")

	// Should contain all path filters
	assert.Contains(t, output, "path /api/v1/*", "Output should contain API path filter")
	assert.Contains(t, output, "path /health", "Output should contain health path filter")

	// Should not show "all TCP connections" for HTTP intercepts
	assert.NotContains(t, output, "all TCP connections", "HTTP intercepts should not show TCP connection message")
}
