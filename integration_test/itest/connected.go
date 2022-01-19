package itest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type connected struct {
	NamespacePair
}

func WithConnection(np NamespacePair, f func(ctx context.Context, ch NamespacePair)) {
	np.HarnessT().Run("Test_Connected", func(t *testing.T) {
		ctx := withT(np.HarnessContext(), t)
		require.NoError(t, np.GeneralError())
		ch := &connected{NamespacePair: np}
		ch.PushHarness(ctx, ch.setup, ch.tearDown)
		defer ch.PopHarness()
		f(ctx, ch)
	})
}

func (ch *connected) setup(ctx context.Context) bool {
	t := getT(ctx)
	// Start once with default user to ensure that the auto-installer can run OK.
	stdout := TelepresenceOk(WithUser(ctx, "default"), "connect")
	require.Contains(t, stdout, "Connected to context default")
	TelepresenceQuitOk(ctx)

	// Connect using telepresence-test-developer user
	stdout = TelepresenceOk(ctx, "connect")
	require.Contains(t, stdout, "Connected to context default")
	TelepresenceOk(ctx, "loglevel", "-d30m", "debug")
	ch.CapturePodLogs(ctx, "app=traffic-manager", "", ch.ManagerNamespace())
	return true
}

func (ch *connected) tearDown(ctx context.Context) {
	TelepresenceQuitOk(ctx)
}
