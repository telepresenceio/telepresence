package cache

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAllDeltaSendAfterSubscriptionClose(t *testing.T) {
	sb := &subscription[string, string]{
		channel: make(chan Delta[string, string], 1),
		doneCh:  make(chan struct{}),
	}
	sb.close()

	ad := allDelta[string, string]{
		snapshot: map[string]string{"key": "value"},
	}
	require.NotPanics(t, func() {
		ad.send(sb)
	})

	_, ok := <-sb.channel
	require.False(t, ok)
}
