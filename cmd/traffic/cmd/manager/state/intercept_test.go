package state

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/clog/testutil"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

// TestAllowGlobalIntercepts_ValidationLogic tests the validation logic
// for the AllowGlobalIntercepts setting without requiring a full agent setup.
func TestAllowGlobalIntercepts_ValidationLogic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		allowGlobal      bool
		mechanism        string
		wiretap          bool
		replace          bool
		expectError      bool
		expectedErrorMsg string
	}{
		{
			name:        "global_intercept_allowed_when_enabled",
			allowGlobal: true,
			mechanism:   "tcp",
			wiretap:     false,
			replace:     false,
			expectError: false,
		},
		{
			name:             "global_intercept_blocked_when_disabled",
			allowGlobal:      false,
			mechanism:        "tcp",
			wiretap:          false,
			replace:          false,
			expectError:      true,
			expectedErrorMsg: "global TCP/UDP intercepts and replaces are disabled",
		},
		{
			name:             "replace_blocked_when_disabled",
			allowGlobal:      false,
			mechanism:        "tcp",
			wiretap:          false,
			replace:          true,
			expectError:      true,
			expectedErrorMsg: "global TCP/UDP intercepts and replaces are disabled",
		},
		{
			name:        "replace_allowed_when_enabled",
			allowGlobal: true,
			mechanism:   "tcp",
			wiretap:     false,
			replace:     true,
			expectError: false,
		},
		{
			name:        "http_intercept_allowed_when_global_disabled",
			allowGlobal: false,
			mechanism:   "http",
			wiretap:     false,
			replace:     false,
			expectError: false,
		},
		{
			name:        "wiretap_allowed_when_global_disabled",
			allowGlobal: false,
			mechanism:   "tcp",
			wiretap:     true,
			replace:     false,
			expectError: false,
		},
		{
			name:        "http_intercept_allowed_when_global_enabled",
			allowGlobal: true,
			mechanism:   "http",
			wiretap:     false,
			replace:     false,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Setup context with appropriate Env
			ctx := testutil.NewContext(t, false)
			env := &managerutil.Env{
				InterceptAllowGlobal: tt.allowGlobal,
			}
			ctx = managerutil.WithEnv(ctx, env)

			// Create a minimal State for testing
			state := &State{
				backgroundCtx:    ctx,
				intercepts:       cache.NewMap[string, *Intercept](interceptEqual, time.Millisecond),
				agents:           cache.NewMap[tunnel.SessionID, *AgentSession](agentsEqual, time.Millisecond),
				clients:          xsync.NewMap[tunnel.SessionID, *ClientSession](),
				workloadWatchers: xsync.NewMap[string, Watcher](),
				timedLogLevel:    log.NewTimedLevel(slog.LevelDebug, clog.SetTreeLevel),
				llSubs:           newLoglevelSubscribers(),
			}

			// Create minimal agent config for testing
			ac := &agentconfig.Sidecar{
				AgentName: "test-agent",
				Namespace: "test-namespace",
			}

			// Create intercept spec
			spec := &rpc.InterceptSpec{
				Mechanism: tt.mechanism,
				Wiretap:   tt.wiretap,
				Replace:   tt.replace,
			}

			// Create minimal CreateInterceptRequest
			cr := &rpc.CreateInterceptRequest{
				InterceptSpec: spec,
			}

			// Create minimal PreparedIntercept
			pi := &rpc.PreparedIntercept{}

			// Call preparePorts which contains our validation logic
			client := &ClientSession{ClientInfo: &rpc.ClientInfo{Name: "client-name"}}
			err := state.checkInterceptConsistency(ac, nil, cr, client, pi)

			// Verify expectations
			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedErrorMsg)
			} else if err != nil {
				// preparePorts will fail for other reasons (no actual ports configured),
				// but we should NOT get the "global intercepts disabled" error
				assert.NotContains(t, err.Error(), "global TCP/UDP intercepts and replaces are disabled",
					"Should not fail due to AllowGlobalIntercepts check")
			}
		})
	}
}

// TestAllowGlobalIntercepts_ErrorMessage tests that the error message
// provides helpful guidance to users.
func TestAllowGlobalIntercepts_ErrorMessage(t *testing.T) {
	t.Parallel()

	ctx := testutil.NewContext(t, false)
	env := &managerutil.Env{
		InterceptAllowGlobal: false,
	}
	ctx = managerutil.WithEnv(ctx, env)

	state := &State{
		backgroundCtx:    ctx,
		intercepts:       cache.NewMap[string, *Intercept](interceptEqual, time.Millisecond),
		agents:           cache.NewMap[tunnel.SessionID, *AgentSession](agentsEqual, time.Millisecond),
		clients:          xsync.NewMap[tunnel.SessionID, *ClientSession](),
		workloadWatchers: xsync.NewMap[string, Watcher](),
		timedLogLevel:    log.NewTimedLevel(slog.LevelDebug, clog.SetTreeLevel),
		llSubs:           newLoglevelSubscribers(),
	}

	ac := &agentconfig.Sidecar{
		AgentName: "test-agent",
		Namespace: "test-namespace",
	}

	spec := &rpc.InterceptSpec{
		Mechanism: "tcp", // Global intercept
		Wiretap:   false,
	}

	cr := &rpc.CreateInterceptRequest{
		InterceptSpec: spec,
	}

	pi := &rpc.PreparedIntercept{}

	client := &ClientSession{ClientInfo: &rpc.ClientInfo{Name: "client-name"}}
	err := state.checkInterceptConsistency(ac, nil, cr, client, pi)

	require.Error(t, err)

	errorMsg := err.Error()

	// Verify error message contains key information
	assert.Contains(t, errorMsg, "global TCP/UDP intercepts and replaces are disabled",
		"Error should clearly state that global intercepts and replaces are disabled")

	// Verify error message suggests HTTP header flag
	assert.Contains(t, errorMsg, "--http-header",
		"Error should suggest using --http-header flag")

	// Verify error message suggests HTTP path flags
	assert.True(t,
		strings.Contains(errorMsg, "--http-path-prefix") ||
			strings.Contains(errorMsg, "--http-path-equal") ||
			strings.Contains(errorMsg, "--http-path-regex") ||
			strings.Contains(errorMsg, "--http-path-"),
		"Error should suggest using HTTP path flags")
}

// TestAllowGlobalIntercepts_DefaultBehavior tests that the default
// value (true) is correctly parsed from environment variables for backward compatibility.
func TestAllowGlobalIntercepts_DefaultBehavior(t *testing.T) {
	t.Parallel()

	// Test that the default value is true when loaded from environment
	envMap := map[string]string{
		// Return minimal required environment variables
		"REGISTRY":                    "ghcr.io/telepresenceio",
		"LOG_LEVEL":                   "info",
		"POD_IP":                      "203.0.113.18",
		"POD_CIDR_STRATEGY":           "auto",
		"SERVER_PORT":                 "8081",
		"GRPC_MAX_RECEIVE_SIZE":       "4Mi",
		"CLIENT_DNS_EXCLUDE_SUFFIXES": ".com .io .net .org .ru",
		"CLIENT_CONNECTION_TTL":       "24h",
	}

	ctx := testutil.NewContext(t, false)
	var err error
	ctx, err = managerutil.LoadEnv(ctx, envMap)
	require.NoError(t, err)

	env := managerutil.GetEnv(ctx)

	// Verify default is true (backward compatible) when loaded from environment
	assert.True(t, env.InterceptAllowGlobal,
		"Default value should be true for backward compatibility")

	state := &State{
		backgroundCtx:    ctx,
		intercepts:       cache.NewMap[string, *Intercept](interceptEqual, time.Millisecond),
		agents:           cache.NewMap[tunnel.SessionID, *AgentSession](agentsEqual, time.Millisecond),
		clients:          xsync.NewMap[tunnel.SessionID, *ClientSession](),
		workloadWatchers: xsync.NewMap[string, Watcher](),
		timedLogLevel:    log.NewTimedLevel(slog.LevelDebug, clog.SetTreeLevel),
		llSubs:           newLoglevelSubscribers(),
	}

	ac := &agentconfig.Sidecar{
		AgentName: "test-agent",
		Namespace: "test-namespace",
	}

	spec := &rpc.InterceptSpec{
		Mechanism: "tcp", // Global intercept
		Wiretap:   false,
	}

	cr := &rpc.CreateInterceptRequest{
		InterceptSpec: spec,
	}

	pi := &rpc.PreparedIntercept{}

	client := &ClientSession{ClientInfo: &rpc.ClientInfo{Name: "client-name"}}
	prepErr := state.checkInterceptConsistency(ac, nil, cr, client, pi)

	// Should not fail due to AllowGlobalIntercepts check
	// (may fail for other reasons like missing port config)
	if prepErr != nil {
		assert.NotContains(t, prepErr.Error(), "global TCP/UDP intercepts and replaces are disabled",
			"Default behavior should allow global intercepts and replaces")
	}
}
