package k8s

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client/portforward"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

func TestManagerAuthError(t *testing.T) {
	tests := []struct {
		name             string
		vi               *manager.VersionInfo2
		hasBearerSource  bool
		hasX509Path      bool
		wantErr          bool
		wantX509Mentions bool
	}{
		{
			name:             "auth required, no bearer source, no x509 path",
			vi:               &manager.VersionInfo2{Name: "traffic-manager", AuthRequired: true},
			hasBearerSource:  false,
			hasX509Path:      false,
			wantErr:          true,
			wantX509Mentions: true,
		},
		{
			name:            "auth required, bearer source present",
			vi:              &manager.VersionInfo2{Name: "traffic-manager", AuthRequired: true},
			hasBearerSource: true,
			hasX509Path:     false,
			wantErr:         false,
		},
		{
			name:            "auth required, x509 path present",
			vi:              &manager.VersionInfo2{Name: "traffic-manager", AuthRequired: true, AuthX509Port: 15007},
			hasBearerSource: false,
			hasX509Path:     true,
			wantErr:         false,
		},
		{
			name:            "auth required, both bearer source and x509 path present",
			vi:              &manager.VersionInfo2{Name: "traffic-manager", AuthRequired: true, AuthX509Port: 15007},
			hasBearerSource: true,
			hasX509Path:     true,
			wantErr:         false,
		},
		{
			name:            "auth supported only",
			vi:              &manager.VersionInfo2{Name: "traffic-manager", AuthSupported: true},
			hasBearerSource: false,
			hasX509Path:     false,
			wantErr:         false,
		},
		{
			name:            "neither supported nor required",
			vi:              &manager.VersionInfo2{Name: "traffic-manager"},
			hasBearerSource: false,
			hasX509Path:     false,
			wantErr:         false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := managerAuthError(tt.vi, tt.hasBearerSource, tt.hasX509Path)
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.ErrorContains(t, err, "bearer token")
			require.ErrorContains(t, err, "security.authentication.mode")
			if tt.wantX509Mentions {
				require.ErrorContains(t, err, "security.authentication.x509.enabled")
			}
		})
	}
}

func TestKnownNameExhaustedError(t *testing.T) {
	err := knownNameExhaustedError("traffic-manager-0")
	require.Error(t, err)
	assert.Equal(t, errcat.User, errcat.GetCategory(err))
	assert.ErrorContains(t, err, "traffic-manager-0")
	assert.ErrorContains(t, err, "clientRbac.legacyAccess")
}

// TestConnectSequence_KnownNameSucceeds verifies that a successful known-name
// attempt short-circuits: discovery and the backoff retry are never invoked.
func TestConnectSequence_KnownNameSucceeds(t *testing.T) {
	res := connectSequence(
		t.Context(),
		time.Second,
		func(ctx context.Context) connectResult { return connectResult{name: "known-manager"} },
		func() (*portforward.PodAddress, error) {
			t.Fatal("discover should not be called")
			return nil, nil
		},
		func(ctx context.Context, pap *portforward.PodAddress) connectResult {
			t.Fatal("connectDiscovered should not be called")
			return connectResult{}
		},
		func(ctx context.Context) connectResult {
			t.Fatal("retryKnownName should not be called")
			return connectResult{}
		},
		func() (bool, error) { return true, nil },
	)
	require.NoError(t, res.err)
	assert.Equal(t, "known-manager", res.name)
}

// TestConnectSequence_FallsBackToDiscovery verifies that a failed known-name
// attempt falls back to discovery, and that a successful discovery result is
// connected to via connectDiscovered.
func TestConnectSequence_FallsBackToDiscovery(t *testing.T) {
	wantPap := &portforward.PodAddress{Name: "traffic-manager-xyz", Namespace: "ambassador"}
	res := connectSequence(
		t.Context(),
		time.Second,
		func(ctx context.Context) connectResult { return connectResult{err: errors.New("dial refused")} },
		func() (*portforward.PodAddress, error) { return wantPap, nil },
		func(ctx context.Context, pap *portforward.PodAddress) connectResult {
			assert.Same(t, wantPap, pap)
			return connectResult{name: "discovered-manager"}
		},
		func(ctx context.Context) connectResult {
			t.Fatal("retryKnownName should not be called")
			return connectResult{}
		},
		func() (bool, error) { return true, nil },
	)
	require.NoError(t, res.err)
	assert.Equal(t, "discovered-manager", res.name)
}

// TestConnectSequence_NotFoundKeepsSetupHint verifies that a NotFound
// discovery error (the manager isn't installed) surfaces the "telepresence
// setup" hint and never triggers the known-name retry.
func TestConnectSequence_NotFoundKeepsSetupHint(t *testing.T) {
	notFound := k8serrors.NewNotFound(schema.GroupResource{Resource: "services"}, "traffic-manager")
	res := connectSequence(
		t.Context(),
		time.Second,
		func(ctx context.Context) connectResult { return connectResult{err: errors.New("dial refused")} },
		func() (*portforward.PodAddress, error) { return nil, notFound },
		func(ctx context.Context, pap *portforward.PodAddress) connectResult {
			t.Fatal("connectDiscovered should not be called")
			return connectResult{}
		},
		func(ctx context.Context) connectResult {
			t.Fatal("retryKnownName should not be called")
			return connectResult{}
		},
		func() (bool, error) { return true, nil },
	)
	require.Error(t, res.err)
	assert.Equal(t, errcat.User, errcat.GetCategory(res.err))
	assert.ErrorContains(t, res.err, "telepresence setup")
}

// TestConnectSequence_ForbiddenRetriesKnownName verifies that a Forbidden
// discovery error -- the minimal-RBAC client case -- retries the known-name
// path instead of failing outright.
func TestConnectSequence_ForbiddenRetriesKnownName(t *testing.T) {
	forbidden := k8serrors.NewForbidden(schema.GroupResource{Resource: "services"}, "traffic-manager", errors.New("denied"))
	res := connectSequence(
		t.Context(),
		time.Second,
		func(ctx context.Context) connectResult { return connectResult{err: errors.New("dial refused")} },
		func() (*portforward.PodAddress, error) { return nil, forbidden },
		func(ctx context.Context, pap *portforward.PodAddress) connectResult {
			t.Fatal("connectDiscovered should not be called")
			return connectResult{}
		},
		func(ctx context.Context) connectResult { return connectResult{name: "retried-manager"} },
		func() (bool, error) { return true, nil },
	)
	require.NoError(t, res.err)
	assert.Equal(t, "retried-manager", res.name)
}

// TestConnectSequence_ForbiddenDeniedFailsFast verifies that when discovery
// is Forbidden and the identity is not permitted to port-forward to the
// known pod either, the refusal is immediate -- no retry loop -- and the
// error carries the forbidden wording.
func TestConnectSequence_ForbiddenDeniedFailsFast(t *testing.T) {
	forbidden := k8serrors.NewForbidden(schema.GroupResource{Resource: "services"}, "traffic-manager", errors.New("denied"))
	res := connectSequence(
		t.Context(),
		time.Second,
		func(ctx context.Context) connectResult { return connectResult{err: errors.New("dial refused")} },
		func() (*portforward.PodAddress, error) { return nil, forbidden },
		func(ctx context.Context, pap *portforward.PodAddress) connectResult {
			t.Fatal("connectDiscovered should not be called")
			return connectResult{}
		},
		func(ctx context.Context) connectResult {
			t.Fatal("retryKnownName should not be called")
			return connectResult{}
		},
		func() (bool, error) { return false, nil },
	)
	require.Error(t, res.err)
	assert.Equal(t, errcat.User, errcat.GetCategory(res.err))
	assert.ErrorContains(t, res.err, "forbidden")
}

// TestConnectSequence_OtherDiscoveryErrorPassesThrough verifies that a
// discovery error which is neither NotFound nor Forbidden is returned
// unchanged, matching today's behavior for that case.
func TestConnectSequence_OtherDiscoveryErrorPassesThrough(t *testing.T) {
	otherErr := errors.New("some other failure")
	res := connectSequence(
		t.Context(),
		time.Second,
		func(ctx context.Context) connectResult { return connectResult{err: errors.New("dial refused")} },
		func() (*portforward.PodAddress, error) { return nil, otherErr },
		func(ctx context.Context, pap *portforward.PodAddress) connectResult {
			t.Fatal("connectDiscovered should not be called")
			return connectResult{}
		},
		func(ctx context.Context) connectResult {
			t.Fatal("retryKnownName should not be called")
			return connectResult{}
		},
		func() (bool, error) { return true, nil },
	)
	require.ErrorIs(t, res.err, otherErr)
}

// TestConnectSequence_ProbeTimeoutBoundsKnownNameOnly verifies that only the
// first known-name attempt is bounded by probeTimeout; the discovery and
// retry paths keep the caller's full dialCtx budget.
func TestConnectSequence_ProbeTimeoutBoundsKnownNameOnly(t *testing.T) {
	dialCtx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	var probeRemaining, retryRemaining time.Duration
	res := connectSequence(
		dialCtx,
		50*time.Millisecond,
		func(ctx context.Context) connectResult {
			dl, ok := ctx.Deadline()
			require.True(t, ok)
			probeRemaining = time.Until(dl)
			return connectResult{err: errors.New("dial refused")}
		},
		func() (*portforward.PodAddress, error) {
			return nil, k8serrors.NewForbidden(schema.GroupResource{Resource: "services"}, "traffic-manager", errors.New("denied"))
		},
		func(ctx context.Context, pap *portforward.PodAddress) connectResult {
			t.Fatal("connectDiscovered should not be called")
			return connectResult{}
		},
		func(ctx context.Context) connectResult {
			dl, ok := ctx.Deadline()
			require.True(t, ok)
			retryRemaining = time.Until(dl)
			return connectResult{name: "retried-manager"}
		},
		func() (bool, error) { return true, nil },
	)
	require.NoError(t, res.err)
	assert.Less(t, probeRemaining, 1*time.Second)
	assert.Greater(t, retryRemaining, 5*time.Second)
}
