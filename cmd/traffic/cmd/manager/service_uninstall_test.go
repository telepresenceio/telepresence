package manager

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
)

// This file covers the read authorization of UninstallAgents.

func (e *readEnv) uninstall(agents ...string) error {
	_, err := e.mgr.UninstallAgents(e.pctx, &rpc.UninstallAgentsRequest{SessionInfo: e.sess, Agents: agents})
	return err
}

// TestAuthorizeUninstall: the authorization decision, which the handler makes
// before it removes anything.
func TestAuthorizeUninstall(t *testing.T) {
	tests := []struct {
		name   string
		caller readCaller
		agents []string
		want   codes.Code
	}{
		{"no grant/all", readCallers[0], nil, codes.PermissionDenied},
		{"no grant/named", readCallers[0], []string{"hello"}, codes.PermissionDenied},
		{"namespace grant/all", readCallers[1], nil, codes.OK},
		{"namespace grant/named", readCallers[1], []string{"hello", "demo"}, codes.OK},
		{"workload grant/all", readCallers[2], nil, codes.PermissionDenied},
		{"workload grant/granted", readCallers[2], []string{"hello"}, codes.OK},
		{"workload grant/other", readCallers[2], []string{"demo"}, codes.PermissionDenied},
		{"workload grant/mixed", readCallers[2], []string{"hello", "demo"}, codes.PermissionDenied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newReadEnv(t, auth.ModeEnforcing, tt.caller.allow)
			_, cs, access, err := sessionService(t, e).clientSessionWithAccess(e.pctx, e.sess)
			require.NoError(t, err)
			err = authorizeUninstall(access, cs.Namespace, tt.agents)
			require.Equal(t, tt.want, status.Code(err), "%v", err)
			if tt.want == codes.PermissionDenied && len(tt.agents) == 0 {
				require.Contains(t, err.Error(), "name the workloads")
			}
		})
	}
	t.Run("not enforcing", func(t *testing.T) {
		e := newReadEnv(t, auth.ModePermissive, isConnectReview)
		_, cs, access, err := sessionService(t, e).clientSessionWithAccess(e.pctx, e.sess)
		require.NoError(t, err)
		require.NoError(t, authorizeUninstall(access, cs.Namespace, nil))
		require.NoError(t, authorizeUninstall(access, cs.Namespace, []string{"hello", "demo"}))
	})
	t.Run("unavailable", func(t *testing.T) {
		e := newReadEnv(t, auth.ModeEnforcing, readCallers[2].allow)
		_, cs, access, err := sessionService(t, e).clientSessionWithAccess(e.pctx, e.sess)
		require.NoError(t, err)
		failReviews(e, isWorkloadReview)
		err = authorizeUninstall(access, cs.Namespace, []string{"hello"})
		require.Equal(t, codes.Unavailable, status.Code(err))
	})
}

// TestUninstallAgents_Handler: the handler refuses before it touches the
// state, and does not refuse what the access allows.
func TestUninstallAgents_Handler(t *testing.T) {
	denied := func(t *testing.T, caller readCaller, agents ...string) {
		e := newReadEnv(t, auth.ModeEnforcing, caller.allow)
		e.restoreAgents()
		require.Equal(t, codes.PermissionDenied, status.Code(e.uninstall(agents...)))
	}
	t.Run("no grant/all", func(t *testing.T) { denied(t, readCallers[0]) })
	t.Run("no grant/named", func(t *testing.T) { denied(t, readCallers[0], "hello") })
	t.Run("workload grant/all", func(t *testing.T) { denied(t, readCallers[2]) })
	t.Run("workload grant/other", func(t *testing.T) { denied(t, readCallers[2], "demo") })
	t.Run("workload grant/mixed", func(t *testing.T) { denied(t, readCallers[2], "hello", "demo") })

	allowed := func(t *testing.T, mode auth.Mode, caller readCaller, agents ...string) {
		e := newReadEnv(t, mode, caller.allow)
		e.restoreAgents()
		require.NotEqual(t, codes.PermissionDenied, status.Code(e.uninstall(agents...)))
	}
	t.Run("namespace grant/all", func(t *testing.T) { allowed(t, auth.ModeEnforcing, readCallers[1]) })
	t.Run("namespace grant/named", func(t *testing.T) { allowed(t, auth.ModeEnforcing, readCallers[1], "hello", "demo") })
	t.Run("workload grant/granted", func(t *testing.T) { allowed(t, auth.ModeEnforcing, readCallers[2], "hello") })
	t.Run("not enforcing/all", func(t *testing.T) { allowed(t, auth.ModePermissive, readCallers[0]) })
	t.Run("not enforcing/named", func(t *testing.T) { allowed(t, auth.ModePermissive, readCallers[0], "hello") })
}
