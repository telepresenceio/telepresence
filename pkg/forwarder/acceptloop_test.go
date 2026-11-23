package forwarder

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type flakyListener struct {
	net.Listener
	mu    sync.Mutex
	calls int
	conn  net.Conn
}

func (l *flakyListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	l.calls++
	n := l.calls
	l.mu.Unlock()
	switch n {
	case 1:
		return nil, errors.New("accept failed")
	case 2:
		return l.conn, nil
	default:
		return nil, net.ErrClosed
	}
}

func TestAcceptLoop_ErrorThenConnection(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	got := make(chan net.Conn, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		AcceptLoop(context.Background(), &flakyListener{conn: c1}, func(_ context.Context, c net.Conn) error {
			got <- c
			return nil
		})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("AcceptLoop did not return")
	}
	select {
	case c := <-got:
		require.NotNil(t, c)
	case <-time.After(5 * time.Second):
		t.Fatal("the connection was not passed to the callback")
	}
}

func TestNextAcceptDelay(t *testing.T) {
	d := time.Duration(0)
	for range 20 {
		d = NextAcceptDelay(d)
	}
	require.Equal(t, time.Second, d)
}
