package client

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClosedChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	so := NewStdOutput(ctx)
	go func() {
		for range so.ResultChannel() {
		}
	}()
	cancel()
	time.Sleep(time.Millisecond)
	_, err := so.Stdout().Write([]byte("boom"))
	require.Error(t, err, io.ErrClosedPipe)
}
