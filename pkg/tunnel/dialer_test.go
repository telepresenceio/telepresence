package tunnel

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

type blockingTunnelProvider struct {
	active int32
	max    int32
	ready  chan struct{}
	once   sync.Once
}

func newBlockingTunnelProvider() *blockingTunnelProvider {
	return &blockingTunnelProvider{
		ready: make(chan struct{}),
	}
}

func (p *blockingTunnelProvider) Tunnel(ctx context.Context, _ ...grpc.CallOption) (Client, error) {
	active := atomic.AddInt32(&p.active, 1)
	for {
		maxActive := atomic.LoadInt32(&p.max)
		if active <= maxActive || atomic.CompareAndSwapInt32(&p.max, maxActive, active) {
			break
		}
	}
	if active == maxConcurrentDialResponders {
		p.once.Do(func() {
			close(p.ready)
		})
	}
	defer atomic.AddInt32(&p.active, -1)
	<-ctx.Done()
	return nil, ctx.Err()
}

type dialRequestStream struct {
	grpc.ClientStream
	requests  <-chan *rpc.DialRequest
	recvCount int32
}

func (s *dialRequestStream) Recv() (*rpc.DialRequest, error) {
	atomic.AddInt32(&s.recvCount, 1)
	dr, ok := <-s.requests
	if !ok {
		return nil, io.EOF
	}
	return dr, nil
}

func TestDialWaitLoopLimitsConcurrentDialResponders(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	requests := make(chan *rpc.DialRequest, maxConcurrentDialResponders+10)
	for i := 0; i < cap(requests); i++ {
		requests <- &rpc.DialRequest{ConnId: []byte(NewZeroID())}
	}

	stream := &dialRequestStream{requests: requests}
	provider := newBlockingTunnelProvider()
	done := make(chan error, 1)
	go func() {
		done <- DialWaitLoop(ctx, AgentToClient, provider, stream, "session")
	}()

	select {
	case <-provider.ready:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %d active dial responders; got %d", maxConcurrentDialResponders, atomic.LoadInt32(&provider.max))
	}

	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&provider.max); got != maxConcurrentDialResponders {
		t.Fatalf("max active dial responders = %d, want %d", got, maxConcurrentDialResponders)
	}
	if got := atomic.LoadInt32(&stream.recvCount); got > maxConcurrentDialResponders+1 {
		t.Fatalf("DialWaitLoop read %d requests while responders were saturated, want at most %d", got, maxConcurrentDialResponders+1)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("DialWaitLoop returned error after cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for DialWaitLoop to exit")
	}
}
