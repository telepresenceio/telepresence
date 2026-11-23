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

	var mu sync.Mutex
	var got []net.Conn
	done := make(chan struct{})
	go func() {
		defer close(done)
		AcceptLoop(context.Background(), &flakyListener{conn: c1}, func(_ context.Context, c net.Conn) error {
			mu.Lock()
			got = append(got, c)
			mu.Unlock()
			return nil
		})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("AcceptLoop did not return")
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, got, 1)
	require.NotNil(t, got[0])
}

func TestNextAcceptDelay(t *testing.T) {
	d := time.Duration(0)
	for range 20 {
		d = NextAcceptDelay(d)
	}
	require.Equal(t, time.Second, d)
}
