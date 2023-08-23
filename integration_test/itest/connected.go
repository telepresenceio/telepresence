package itest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type connected struct {
	NamespacePair
}

func WithConnected(np NamespacePair, f func(ctx context.Context, ch NamespacePair)) {
	np.HarnessT().Run("Test_Connected", func(t *testing.T) {
		ctx := WithT(np.HarnessContext(), t)
		require.NoError(t, np.GeneralError())
		ch := &connected{NamespacePair: np}
		ch.PushHarness(ctx, ch.setup, ch.tearDown)
		defer ch.PopHarness()
		f(ctx, ch)
	})
}

func (ch *connected) setup(ctx context.Context) bool {
	t := getT(ctx)
	// Connect using telepresence-test-developer user
	stdout, _, err := Telepresence(ctx, "connect", "--namespace", ch.AppNamespace(), "--manager-namespace", ch.ManagerNamespace())
	assert.NoError(t, err)
	assert.Contains(t, stdout, "Connected to context")
	if t.Failed() {
		return false
	}
	_, _, err = Telepresence(ctx, "loglevel", "-d30m", "debug")
	assert.NoError(t, err)
	return !t.Failed()
}

func (ch *connected) tearDown(ctx context.Context) {
	TelepresenceQuitOk(ctx)
}
