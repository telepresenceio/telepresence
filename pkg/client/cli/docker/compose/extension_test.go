package compose

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

func TestAmendInterceptSpec_MechanismSetToHTTP(t *testing.T) {
	tests := []struct {
		name              string
		httpFilters       map[string]string
		httpPaths         []string
		httpPathPrefixes  []string
		httpPathRegexps   []string
		expectedMechanism string
	}{
		{
			name:              "no filters leaves mechanism as tcp",
			expectedMechanism: "tcp",
		},
		{
			name:              "header filter switches mechanism to http",
			httpFilters:       map[string]string{"x-dev-id": "jdoe"},
			expectedMechanism: "http",
		},
		{
			name:              "multiple header filters switch mechanism to http",
			httpFilters:       map[string]string{"x-dev-id": "jdoe", "x-env": "staging"},
			expectedMechanism: "http",
		},
		{
			name:              "exact path filter switches mechanism to http",
			httpPaths:         []string{"/api/v1/users"},
			expectedMechanism: "http",
		},
		{
			name:              "path prefix filter switches mechanism to http",
			httpPathPrefixes:  []string{"/api/"},
			expectedMechanism: "http",
		},
		{
			name:              "path regex filter switches mechanism to http",
			httpPathRegexps:   []string{`/api/v[0-9]+/.*`},
			expectedMechanism: "http",
		},
		{
			name:              "headers and paths together switch mechanism to http",
			httpFilters:       map[string]string{"x-dev-id": "jdoe"},
			httpPathPrefixes:  []string{"/api/"},
			expectedMechanism: "http",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ext := &httpFilterExtension{
				HttpFilters:  tt.httpFilters,
				Paths:        tt.httpPaths,
				PathPrefixes: tt.httpPathPrefixes,
				PathRegexps:  tt.httpPathRegexps,
				Ports:        []types.PortMapping{"3005:3005"},
			}

			spec := &manager.InterceptSpec{Mechanism: "tcp"}
			err := ext.amendInterceptSpec(spec)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedMechanism, spec.Mechanism)
		})
	}
}

func TestAmendInterceptSpec_HeaderFiltersPopulated(t *testing.T) {
	filters := map[string]string{"x-dev-id": "jdoe", "x-env": "staging"}
	ext := &httpFilterExtension{
		HttpFilters: filters,
		Ports:       []types.PortMapping{"3005:3005"},
	}

	spec := &manager.InterceptSpec{Mechanism: "tcp"}
	err := ext.amendInterceptSpec(spec)
	require.NoError(t, err)
	assert.Equal(t, filters, spec.HeaderFilters)
}

func TestAmendInterceptSpec_NoPorts_ReturnsError(t *testing.T) {
	ext := &httpFilterExtension{
		HttpFilters: map[string]string{"x-dev-id": "jdoe"},
	}

	spec := &manager.InterceptSpec{Mechanism: "tcp"}
	err := ext.amendInterceptSpec(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires at least one port")
}
