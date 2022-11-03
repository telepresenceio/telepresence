package client

import (
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClosedChannel(t *testing.T) {
	so := NewStdOutput()
	go func() {
		for range so.ResultChannel() {
		}
	}()
	so.Finish(nil)
	time.Sleep(time.Millisecond)
	_, err := so.Stdout().Write([]byte("boom"))
	require.Error(t, err, io.ErrClosedPipe)
}
