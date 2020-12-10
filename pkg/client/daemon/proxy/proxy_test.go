package proxy

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

// TestProxy_Run tests that no more than the given number of connections
// are proxied in parallel.
func TestProxy_Run(t *testing.T) {
	count := 0
	latch := sync.WaitGroup{}
	latch.Add(1)
	connHandler := func(pxy *Proxy, c context.Context, conn *net.TCPConn) {
		count++
		latch.Wait()
		_ = conn.Close()
	}

	c, cancel := context.WithCancel(context.Background())
	ln, err := net.Listen("tcp", ":1234")
	if err != nil {
		t.Fatal(err)
	}
	pxy := &Proxy{listener: ln, connHandler: connHandler}

	connLimit := 10
	go pxy.Run(c, int64(connLimit))

	wg := sync.WaitGroup{}
	wg.Add(connLimit * 2)
	for i := 0; i < connLimit*2; i++ {
		go func() {
			conn, err := net.Dial("tcp", ":1234")
			if err != nil {
				wg.Done()
				t.Error(err)
				return
			}
			time.Sleep(10 * time.Millisecond)
			wg.Done()
			<-c.Done()
			_ = conn.Close()
		}()
	}
	wg.Wait()

	// Check that no more than connLimit connections are proxied
	if count != connLimit {
		t.Errorf("expected %d connections, got %d", connLimit, count)
	}

	// Release latch (close currently proxied connections)
	latch.Done()
	time.Sleep(10 * time.Millisecond)

	// Check that the rest got proxied
	if count != connLimit*2 {
		t.Errorf("expected %d connections, got %d", connLimit, count)
	}
	cancel()
}
