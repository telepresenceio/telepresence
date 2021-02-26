package proxy

import (
	"context"
	"fmt"
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

	connClosed := make(chan bool)
	connHandler := func(pxy *Proxy, c context.Context, conn *net.TCPConn) {
		count++
		latch.Wait()
		_ = conn.Close()
		connClosed <- true
	}

	c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	pxy := &Proxy{listener: ln, connHandler: connHandler}
	port, err := pxy.ListenerPort()
	if err != nil {
		t.Fatal(err)
	}

	connLimit := 10
	go pxy.Run(c, int64(connLimit))

	wg := sync.WaitGroup{}
	wg.Add(connLimit * 2)
	for i := 0; i < connLimit*2; i++ {
		go func() {
			conn, err := net.Dial("tcp", fmt.Sprintf(":%d", port))
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
	for connsLeft := connLimit * 2; connsLeft > 0; connsLeft-- {
		select {
		case <-c.Done():
			t.Fatalf("expected %d connections, got %d", connLimit, count)
		case <-connClosed:
		}
	}
}
