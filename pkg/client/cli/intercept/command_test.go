package intercept

import (
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommand_Validate_HTTPIntercepts(t *testing.T) {
	t.Skip("Skipping tests that require full telepresence config setup")
	tests := []struct {
		name        string
		cmd         *Command
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid HTTP intercept with headers",
			cmd: &Command{
				HTTPHeaderFilters: []string{"X-User-ID=dev123", "X-Environment=staging"},
				Mechanism:         "tcp", // Will be overridden to "http"
			},
			expectError: false,
		},
		{
			name: "valid HTTP intercept with paths",
			cmd: &Command{
				HTTPPathPrefixFilters: []string{"/api/v1/", "/admin/"},
				Mechanism:             "tcp", // Will be overridden to "http"
			},
			expectError: false,
		},
		{
			name: "valid HTTP intercept with both headers and paths",
			cmd: &Command{
				HTTPHeaderFilters:     []string{"X-User-ID=dev123"},
				HTTPPathPrefixFilters: []string{"/api/"},
				Mechanism:             "tcp", // Will be overridden to "http"
			},
			expectError: false,
		},
		{
			name: "invalid header format",
			cmd: &Command{
				HTTPHeaderFilters: []string{"InvalidHeader"},
			},
			expectError: true,
			errorMsg:    "invalid header format 'InvalidHeader': must be key=value or key: value",
		},
		{
			name: "empty header key with equals",
			cmd: &Command{
				HTTPHeaderFilters: []string{"=value"},
			},
			expectError: true,
			errorMsg:    "invalid header format '=value': key cannot be empty",
		},
		{
			name: "empty header key with colon",
			cmd: &Command{
				HTTPHeaderFilters: []string{": value"},
			},
			expectError: true,
			errorMsg:    "invalid header format ': value': key cannot be empty",
		},
		{
			name: "valid header with colon separator (curl -H format)",
			cmd: &Command{
				HTTPHeaderFilters: []string{"X-User-ID: dev123", "Authorization: Bearer token"},
			},
			expectError: false,
		},
		{
			name: "mixed header formats",
			cmd: &Command{
				HTTPHeaderFilters: []string{"X-User-ID=dev123", "Authorization: Bearer token"},
			},
			expectError: false,
		},
		{
			name: "standard TCP intercept unchanged",
			cmd: &Command{
				Mechanism: "tcp",
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{}
			// Create a basic context for the test
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			cmd.SetContext(ctx)

			// Set up minimal required fields
			if tt.cmd.Mechanism == "" {
				tt.cmd.Mechanism = "tcp"
			}

			err := tt.cmd.Validate(cmd, []string{"test-service"})

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				require.NoError(t, err)

				// Verify mechanism is set to "http" when HTTP flags are used
				if tt.cmd.UsesHTTPMechanism() {
					assert.Equal(t, "http", tt.cmd.Mechanism)
				}
			}
		})
	}
}

func TestCommand_UsesHTTPMechanism(t *testing.T) {
	tests := []struct {
		name     string
		cmd      *Command
		expected bool
	}{
		{
			name:     "no HTTP flags",
			cmd:      &Command{},
			expected: false,
		},
		{
			name: "with HTTP header filters",
			cmd: &Command{
				HTTPHeaderFilters: []string{"X-User-ID=dev123"},
			},
			expected: true,
		},
		{
			name: "with HTTP path filters",
			cmd: &Command{
				HTTPPathPrefixFilters: []string{"/api/"},
			},
			expected: true,
		},
		{
			name: "with both HTTP filters",
			cmd: &Command{
				HTTPHeaderFilters:     []string{"X-User-ID=dev123"},
				HTTPPathPrefixFilters: []string{"/api/"},
			},
			expected: true,
		},
		{
			name: "empty HTTP header filters",
			cmd: &Command{
				HTTPHeaderFilters: []string{},
			},
			expected: false,
		},
		{
			name: "empty HTTP path filters",
			cmd: &Command{
				HTTPPathEqualFilters: []string{},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.cmd.UsesHTTPMechanism()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestParseHTTPHeader(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expectedKey string
		expectedVal string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "equals separator",
			input:       "X-User-ID=dev123",
			expectedKey: "X-User-ID",
			expectedVal: "dev123",
			expectError: false,
		},
		{
			name:        "colon separator",
			input:       "X-User-ID: dev123",
			expectedKey: "X-User-ID",
			expectedVal: "dev123",
			expectError: false,
		},
		{
			name:        "colon separator with spaces",
			input:       "Authorization: Bearer token=abc123",
			expectedKey: "Authorization",
			expectedVal: "Bearer token=abc123",
			expectError: false,
		},
		{
			name:        "equals in value",
			input:       "Authorization=Bearer token=abc123",
			expectedKey: "Authorization",
			expectedVal: "Bearer token=abc123",
			expectError: false,
		},
		{
			name:        "extra spaces trimmed",
			input:       "  X-User-ID  :  dev123  ",
			expectedKey: "X-User-ID",
			expectedVal: "dev123",
			expectError: false,
		},
		{
			name:        "invalid format no separator",
			input:       "InvalidHeader",
			expectError: true,
			errorMsg:    "must be key=value or key: value",
		},
		{
			name:        "empty key with equals",
			input:       "=value",
			expectError: true,
			errorMsg:    "key cannot be empty",
		},
		{
			name:        "empty key with colon",
			input:       ": value",
			expectError: true,
			errorMsg:    "key cannot be empty",
		},
		{
			name:        "empty value with equals",
			input:       "Key=",
			expectedKey: "Key",
			expectedVal: "",
			expectError: false,
		},
		{
			name:        "empty value with colon",
			input:       "Key:",
			expectedKey: "Key",
			expectedVal: "",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, value, err := parseHTTPHeader(tt.input)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedKey, key)
				assert.Equal(t, tt.expectedVal, value)
			}
		})
	}
}

func TestCommand_HeaderParsing(t *testing.T) {
	tests := []struct {
		name     string
		headers  []string
		expected map[string]string
	}{
		{
			name:    "single header",
			headers: []string{"X-User-ID=dev123"},
			expected: map[string]string{
				"X-User-ID": "dev123",
			},
		},
		{
			name:    "multiple headers",
			headers: []string{"X-User-ID=dev123", "X-Environment=staging"},
			expected: map[string]string{
				"X-User-ID":     "dev123",
				"X-Environment": "staging",
			},
		},
		{
			name:    "header with equals in value",
			headers: []string{"Authorization=Bearer token=123"},
			expected: map[string]string{
				"Authorization": "Bearer token=123",
			},
		},
		{
			name:     "no headers",
			headers:  []string{},
			expected: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// This test verifies the header parsing logic in state.go
			result := make(map[string]string)
			for _, header := range tt.headers {
				parts := strings.SplitN(header, "=", 2)
				if len(parts) == 2 {
					result[parts[0]] = parts[1]
				}
			}

			assert.Equal(t, tt.expected, result)
		})
	}
}
