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

// fakeDialer is a managerDialer whose behaviors are per-test funcs; an
// unset func fails the test, so a path the test expects not to run is
// asserted by omission.
type fakeDialer struct {
	t        *testing.T
	dial     func(context.Context, *portforward.PodAddress) connectResult
	discover func() (*portforward.PodAddress, error)
	retry    func(context.Context, *portforward.PodAddress) connectResult
	canReach func() (bool, error)
}

func (d fakeDialer) dialPod(ctx context.Context, pap *portforward.PodAddress) connectResult {
	if d.dial == nil {
		d.t.Fatal("dialPod should not be called")
	}
	return d.dial(ctx, pap)
}

func (d fakeDialer) discoverPod() (*portforward.PodAddress, error) {
	if d.discover == nil {
		d.t.Fatal("discoverPod should not be called")
	}
	return d.discover()
}

func (d fakeDialer) retryKnownName(ctx context.Context, pap *portforward.PodAddress) connectResult {
	if d.retry == nil {
		d.t.Fatal("retryKnownName should not be called")
	}
	return d.retry(ctx, pap)
}

func (d fakeDialer) canReachKnownName() (bool, error) {
	if d.canReach == nil {
		d.t.Fatal("canReachKnownName should not be called")
	}
	return d.canReach()
}

func knownPapFixture() *portforward.PodAddress {
	return &portforward.PodAddress{Name: "traffic-manager-0", Namespace: "ambassador", NoLookup: true}
}

// TestConnectSequence_KnownNameSucceeds verifies that a successful known-name
// attempt short-circuits: discovery and the backoff retry are never invoked.
func TestConnectSequence_KnownNameSucceeds(t *testing.T) {
	res := connectSequence(t.Context(), time.Second, fakeDialer{
		t: t,
		dial: func(context.Context, *portforward.PodAddress) connectResult {
			return connectResult{name: "known-manager"}
		},
	}, knownPapFixture())
	require.NoError(t, res.err)
	assert.Equal(t, "known-manager", res.name)
}

// TestConnectSequence_FallsBackToDiscovery verifies that a failed known-name
// attempt falls back to discovery, and that a successful discovery result is
// connected to via dialPod.
func TestConnectSequence_FallsBackToDiscovery(t *testing.T) {
	wantPap := &portforward.PodAddress{Name: "traffic-manager-xyz", Namespace: "ambassador"}
	known := knownPapFixture()
	firstCall := true
	res := connectSequence(t.Context(), time.Second, fakeDialer{
		t: t,
		dial: func(_ context.Context, pap *portforward.PodAddress) connectResult {
			if firstCall {
				firstCall = false
				assert.Same(t, known, pap)
				return connectResult{err: errors.New("dial refused")}
			}
			assert.Same(t, wantPap, pap)
			return connectResult{name: "discovered-manager"}
		},
		discover: func() (*portforward.PodAddress, error) { return wantPap, nil },
	}, known)
	require.NoError(t, res.err)
	assert.Equal(t, "discovered-manager", res.name)
}

// TestConnectSequence_NotFoundKeepsSetupHint verifies that a NotFound
// discovery error (the manager isn't installed) surfaces the "telepresence
// setup" hint and never triggers the known-name retry.
func TestConnectSequence_NotFoundKeepsSetupHint(t *testing.T) {
	notFound := k8serrors.NewNotFound(schema.GroupResource{Resource: "services"}, "traffic-manager")
	res := connectSequence(t.Context(), time.Second, fakeDialer{
		t: t,
		dial: func(context.Context, *portforward.PodAddress) connectResult {
			return connectResult{err: errors.New("dial refused")}
		},
		discover: func() (*portforward.PodAddress, error) { return nil, notFound },
	}, knownPapFixture())
	require.Error(t, res.err)
	assert.Equal(t, errcat.User, errcat.GetCategory(res.err))
	assert.ErrorContains(t, res.err, "telepresence setup")
}

// TestConnectSequence_ForbiddenRetriesKnownName verifies that a Forbidden
// discovery error -- the minimal-RBAC client case -- retries the known-name
// path instead of failing outright.
func TestConnectSequence_ForbiddenRetriesKnownName(t *testing.T) {
	forbidden := k8serrors.NewForbidden(schema.GroupResource{Resource: "services"}, "traffic-manager", errors.New("denied"))
	res := connectSequence(t.Context(), time.Second, fakeDialer{
		t: t,
		dial: func(context.Context, *portforward.PodAddress) connectResult {
			return connectResult{err: errors.New("dial refused")}
		},
		discover: func() (*portforward.PodAddress, error) { return nil, forbidden },
		retry: func(context.Context, *portforward.PodAddress) connectResult {
			return connectResult{name: "retried-manager"}
		},
		canReach: func() (bool, error) { return true, nil },
	}, knownPapFixture())
	require.NoError(t, res.err)
	assert.Equal(t, "retried-manager", res.name)
}

// TestConnectSequence_ForbiddenDeniedFailsFast fails immediately, with no
// retry, when discovery is Forbidden and known-name port-forward is also denied.
func TestConnectSequence_ForbiddenDeniedFailsFast(t *testing.T) {
	forbidden := k8serrors.NewForbidden(schema.GroupResource{Resource: "services"}, "traffic-manager", errors.New("denied"))
	res := connectSequence(t.Context(), time.Second, fakeDialer{
		t: t,
		dial: func(context.Context, *portforward.PodAddress) connectResult {
			return connectResult{err: errors.New("dial refused")}
		},
		discover: func() (*portforward.PodAddress, error) { return nil, forbidden },
		canReach: func() (bool, error) { return false, nil },
	}, knownPapFixture())
	require.Error(t, res.err)
	assert.Equal(t, errcat.User, errcat.GetCategory(res.err))
	assert.ErrorContains(t, res.err, "forbidden")
}

// TestConnectSequence_OtherDiscoveryErrorPassesThrough verifies that a
// discovery error which is neither NotFound nor Forbidden is returned
// unchanged, matching today's behavior for that case.
func TestConnectSequence_OtherDiscoveryErrorPassesThrough(t *testing.T) {
	otherErr := errors.New("some other failure")
	res := connectSequence(t.Context(), time.Second, fakeDialer{
		t: t,
		dial: func(context.Context, *portforward.PodAddress) connectResult {
			return connectResult{err: errors.New("dial refused")}
		},
		discover: func() (*portforward.PodAddress, error) { return nil, otherErr },
	}, knownPapFixture())
	require.ErrorIs(t, res.err, otherErr)
}

// TestConnectSequence_ProbeTimeoutBoundsKnownNameOnly verifies that only the
// first known-name attempt is bounded by probeTimeout; the discovery and
// retry paths keep the caller's full dialCtx budget.
func TestConnectSequence_ProbeTimeoutBoundsKnownNameOnly(t *testing.T) {
	dialCtx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	var probeRemaining, retryRemaining time.Duration
	firstCall := true
	res := connectSequence(dialCtx, 50*time.Millisecond, fakeDialer{
		t: t,
		dial: func(ctx context.Context, _ *portforward.PodAddress) connectResult {
			require.True(t, firstCall)
			firstCall = false
			dl, ok := ctx.Deadline()
			require.True(t, ok)
			probeRemaining = time.Until(dl)
			return connectResult{err: errors.New("dial refused")}
		},
		discover: func() (*portforward.PodAddress, error) {
			return nil, k8serrors.NewForbidden(schema.GroupResource{Resource: "services"}, "traffic-manager", errors.New("denied"))
		},
		retry: func(ctx context.Context, _ *portforward.PodAddress) connectResult {
			dl, ok := ctx.Deadline()
			require.True(t, ok)
			retryRemaining = time.Until(dl)
			return connectResult{name: "retried-manager"}
		},
		canReach: func() (bool, error) { return true, nil },
	}, knownPapFixture())
	require.NoError(t, res.err)
	assert.Less(t, probeRemaining, 1*time.Second)
	assert.Greater(t, retryRemaining, 5*time.Second)
}
