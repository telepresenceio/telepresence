package state

import (
	"strings"
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/watchable"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/workload"
)

// TestAllowGlobalIntercepts_ValidationLogic tests the validation logic
// for the AllowGlobalIntercepts setting without requiring a full agent setup.
func TestAllowGlobalIntercepts_ValidationLogic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		allowGlobal       bool
		mechanism         string
		wiretap           bool
		expectError       bool
		expectedErrorMsg  string
	}{
		{
			name:        "global_intercept_allowed_when_enabled",
			allowGlobal: true,
			mechanism:   "tcp",
			wiretap:     false,
			expectError: false,
		},
		{
			name:             "global_intercept_blocked_when_disabled",
			allowGlobal:      false,
			mechanism:        "tcp",
			wiretap:          false,
			expectError:      true,
			expectedErrorMsg: "global TCP/UDP intercepts are disabled",
		},
		{
			name:        "http_intercept_allowed_when_global_disabled",
			allowGlobal: false,
			mechanism:   "http",
			wiretap:     false,
			expectError: false,
		},
		{
			name:        "wiretap_allowed_when_global_disabled",
			allowGlobal: false,
			mechanism:   "tcp",
			wiretap:     true,
			expectError: false,
		},
		{
			name:        "http_intercept_allowed_when_global_enabled",
			allowGlobal: true,
			mechanism:   "http",
			wiretap:     false,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Setup context with appropriate Env
			ctx := dlog.NewTestContext(t, false)
			env := &managerutil.Env{
				AllowGlobalIntercepts: tt.allowGlobal,
			}
			ctx = managerutil.WithEnv(ctx, env)

			// Create a minimal State for testing
			state := &State{
				backgroundCtx:    ctx,
				intercepts:       watchable.NewMap[string, *Intercept](interceptEqual, time.Millisecond),
				agents:           watchable.NewMap[tunnel.SessionID, *AgentSession](agentsEqual, time.Millisecond),
				clients:          xsync.NewMap[tunnel.SessionID, *ClientSession](),
				workloadWatchers: xsync.NewMap[string, workload.Watcher](),
				timedLogLevel:    log.NewTimedLevel("debug", log.SetLevel),
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
			}

			// Create minimal CreateInterceptRequest
			cr := &rpc.CreateInterceptRequest{
				InterceptSpec: spec,
			}

			// Create minimal PreparedIntercept
			pi := &rpc.PreparedIntercept{}

			// Call preparePorts which contains our validation logic
			err := state.preparePorts(ac, nil, cr, pi)

			// Verify expectations
			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedErrorMsg)
			} else {
				// preparePorts will fail for other reasons (no actual ports configured),
				// but we should NOT get the "global intercepts disabled" error
				if err != nil {
					assert.NotContains(t, err.Error(), "global TCP/UDP intercepts are disabled",
						"Should not fail due to AllowGlobalIntercepts check")
				}
			}
		})
	}
}

// TestAllowGlobalIntercepts_ErrorMessage tests that the error message
// provides helpful guidance to users.
func TestAllowGlobalIntercepts_ErrorMessage(t *testing.T) {
	t.Parallel()

	ctx := dlog.NewTestContext(t, false)
	env := &managerutil.Env{
		AllowGlobalIntercepts: false,
	}
	ctx = managerutil.WithEnv(ctx, env)

	state := &State{
		backgroundCtx:    ctx,
		intercepts:       watchable.NewMap[string, *Intercept](interceptEqual, time.Millisecond),
		agents:           watchable.NewMap[tunnel.SessionID, *AgentSession](agentsEqual, time.Millisecond),
		clients:          xsync.NewMap[tunnel.SessionID, *ClientSession](),
		workloadWatchers: xsync.NewMap[string, workload.Watcher](),
		timedLogLevel:    log.NewTimedLevel("debug", log.SetLevel),
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

	err := state.preparePorts(ac, nil, cr, pi)

	require.Error(t, err)

	errorMsg := err.Error()

	// Verify error message contains key information
	assert.Contains(t, errorMsg, "global TCP/UDP intercepts are disabled",
		"Error should clearly state that global intercepts are disabled")

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
	lookupFunc := func(key string) (string, bool) {
		// Return minimal required environment variables
		switch key {
		case "REGISTRY":
			return "ghcr.io/telepresenceio", true
		case "LOG_LEVEL":
			return "info", true
		case "POD_IP":
			return "203.0.113.18", true
		case "POD_CIDR_STRATEGY":
			return "auto", true
		case "SERVER_PORT":
			return "8081", true
		case "GRPC_MAX_RECEIVE_SIZE":
			return "4Mi", true
		case "CLIENT_DNS_EXCLUDE_SUFFIXES":
			return ".com .io .net .org .ru", true
		case "CLIENT_CONNECTION_TTL":
			return "24h", true
		default:
			return "", false
		}
	}

	ctx := dlog.NewTestContext(t, false)
	var err error
	ctx, err = managerutil.LoadEnv(ctx, lookupFunc)
	require.NoError(t, err)

	env := managerutil.GetEnv(ctx)

	// Verify default is true (backward compatible) when loaded from environment
	assert.True(t, env.AllowGlobalIntercepts,
		"Default value should be true for backward compatibility")

	state := &State{
		backgroundCtx:    ctx,
		intercepts:       watchable.NewMap[string, *Intercept](interceptEqual, time.Millisecond),
		agents:           watchable.NewMap[tunnel.SessionID, *AgentSession](agentsEqual, time.Millisecond),
		clients:          xsync.NewMap[tunnel.SessionID, *ClientSession](),
		workloadWatchers: xsync.NewMap[string, workload.Watcher](),
		timedLogLevel:    log.NewTimedLevel("debug", log.SetLevel),
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

	prepErr := state.preparePorts(ac, nil, cr, pi)

	// Should not fail due to AllowGlobalIntercepts check
	// (may fail for other reasons like missing port config)
	if prepErr != nil {
		assert.NotContains(t, prepErr.Error(), "global TCP/UDP intercepts are disabled",
			"Default behavior should allow global intercepts")
	}
}
