package state

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func newTestSession(t *testing.T) *ClientSession {
	t.Helper()
	return newClientSessionState(context.Background(), "sess", &rpc.ClientInfo{Name: "c"}, time.Now())
}

var (
	errDenied      = status.Error(codes.PermissionDenied, "denied")
	errUnavailable = status.Error(codes.Unavailable, "unavailable")
)

func allows(t *testing.T, a ReadAccess, w string) bool {
	t.Helper()
	ok, err := a.Allows(w)
	require.NoError(t, err)
	return ok
}

func readAccess(t *testing.T, cs *ClientSession, ns string, nsp func() error, wlp func(string) error) ReadAccess {
	t.Helper()
	a, err := cs.ReadAccess(ns, nsp, wlp)
	require.NoError(t, err)
	return a
}

func TestReadAccess_NamespaceAllowedIsCached(t *testing.T) {
	cs := newTestSession(t)
	var nsCalls, wlCalls int
	ns := func() error { nsCalls++; return nil }
	wl := func(string) error { wlCalls++; return errDenied }
	for range 3 {
		a := readAccess(t, cs, "a", ns, wl)
		require.True(t, a.All())
		require.True(t, allows(t, a, "x"))
	}
	require.Equal(t, 1, nsCalls)
	require.Zero(t, wlCalls)
}

func TestReadAccess_WorkloadLevel(t *testing.T) {
	cs := newTestSession(t)
	var nsCalls int
	wlCalls := map[string]int{}
	ns := func() error { nsCalls++; return errDenied }
	wl := func(w string) error {
		wlCalls[w]++
		if w == "ok" {
			return nil
		}
		return errDenied
	}
	for range 3 {
		a := readAccess(t, cs, "a", ns, wl)
		require.False(t, a.All())
		require.True(t, allows(t, a, "ok"))
		require.False(t, allows(t, a, "no"))
	}
	require.Equal(t, 1, nsCalls)
	require.Equal(t, map[string]int{"ok": 1, "no": 1}, wlCalls)

	// Another namespace has its own verdicts.
	b := readAccess(t, cs, "b", func() error { return errDenied }, func(string) error { return errDenied })
	require.False(t, allows(t, b, "ok"))
}

func TestReadAccess_UnavailableNotCached(t *testing.T) {
	cs := newTestSession(t)
	nsErr := errUnavailable
	nsCalls := 0
	ns := func() error { nsCalls++; return nsErr }
	wlErr := errUnavailable
	wlCalls := 0
	wl := func(string) error { wlCalls++; return wlErr }

	_, err := cs.ReadAccess("a", ns, wl)
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.Zero(t, wlCalls, "the workload probe runs only after a definite namespace denial")

	nsErr = errDenied
	a := readAccess(t, cs, "a", ns, wl)
	require.Equal(t, 2, nsCalls)
	for range 2 {
		ok, err := a.Allows("w")
		require.False(t, ok)
		require.Equal(t, codes.Unavailable, status.Code(err))
	}
	require.Equal(t, 2, wlCalls, "an Unavailable workload probe is asked again")

	wlErr = nil
	require.True(t, allows(t, readAccess(t, cs, "a", ns, wl), "w"))
	require.True(t, allows(t, readAccess(t, cs, "a", ns, wl), "w"))
	require.Equal(t, 3, wlCalls)
	require.Equal(t, 2, nsCalls)
}
