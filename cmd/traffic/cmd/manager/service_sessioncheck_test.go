package manager

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

func sessionService(t *testing.T, e *readEnv) *service {
	t.Helper()
	s, ok := e.mgr.(*service)
	require.True(t, ok)
	return s
}

// TestSessionChecks: ownedClientSession needs ownership only,
// clientSessionWithAccess returns the namespace access, and ensureClientSession
// requires access to the whole namespace.
func TestSessionChecks(t *testing.T) {
	type outcome struct {
		all       bool
		allowsHel bool
		strict    codes.Code
	}
	tests := []struct {
		name   string
		mode   auth.Mode
		caller readCaller
		want   outcome
	}{
		{"no grant", auth.ModeEnforcing, readCallers[0], outcome{false, false, codes.PermissionDenied}},
		{"namespace grant", auth.ModeEnforcing, readCallers[1], outcome{true, true, codes.OK}},
		{"workload grant", auth.ModeEnforcing, readCallers[2], outcome{false, true, codes.PermissionDenied}},
		{"permissive", auth.ModePermissive, readCallers[0], outcome{true, true, codes.OK}},
		{"disabled", auth.ModeDisabled, readCallers[0], outcome{true, true, codes.OK}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newReadEnv(t, tt.mode, tt.caller.allow)
			s := sessionService(t, e)

			_, cs, err := s.ownedClientSession(e.pctx, e.sess)
			require.NoError(t, err)
			require.NotNil(t, cs)

			_, cs, access, err := s.clientSessionWithAccess(e.pctx, e.sess)
			require.NoError(t, err)
			require.NotNil(t, cs)
			require.Equal(t, tt.want.all, access.All())
			ok, err := access.Allows("hello")
			require.NoError(t, err)
			require.Equal(t, tt.want.allowsHel, ok)

			ctx, cs, err := s.ensureClientSession(e.pctx, e.sess)
			require.Equal(t, tt.want.strict, status.Code(err))
			if tt.want.strict == codes.OK {
				require.NotNil(t, cs)
				require.Equal(t, tunnel.SessionID(e.sess.SessionId), managerutil.GetSessionID(ctx))
			} else {
				require.Nil(t, cs)
			}
		})
	}
}

// TestSessionChecks_Unavailable: a namespace review that cannot be performed is
// returned as Unavailable by the checks that review.
func TestSessionChecks_Unavailable(t *testing.T) {
	e := newReadEnv(t, auth.ModeEnforcing, readCallers[1].allow)
	s := sessionService(t, e)
	failReviews(e, isNamespaceReview)

	_, cs, _, err := s.clientSessionWithAccess(e.pctx, e.sess)
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.Nil(t, cs)
	_, _, err = s.ensureClientSession(e.pctx, e.sess)
	require.Equal(t, codes.Unavailable, status.Code(err))
	_, _, err = s.ownedClientSession(e.pctx, e.sess)
	require.NoError(t, err)
}
