package tunnel

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/ipproto"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
)

type uni struct {
	done <-chan struct{}
	ch   chan *manager.TunnelMessage
}

type bidi struct {
	cToS *uni
	sToC *uni
}

func newUni(bufSize int, done <-chan struct{}) *uni {
	return &uni{ch: make(chan *manager.TunnelMessage, bufSize), done: done}
}

func newBidi(bufSize int, done <-chan struct{}) *bidi {
	return &bidi{cToS: newUni(bufSize, done), sToC: newUni(bufSize, done)}
}

func (t *uni) recv() (*manager.TunnelMessage, error) {
	select {
	case <-t.done:
		return nil, context.Canceled
	case m := <-t.ch:
		if m == nil {
			return nil, net.ErrClosed
		}
		// Simulate a network latency of one microsecond per byte
		time.Sleep(time.Duration(len(m.Payload)) * time.Microsecond)
		return m, nil
	}
}

func (t *uni) send(msg *manager.TunnelMessage) error {
	select {
	case <-t.done:
		return context.Canceled
	case t.ch <- msg:
		return nil
	}
}

func (t *uni) close() error {
	close(t.ch)
	return nil
}

func (t *bidi) clientSide() GRPCClientStream {
	return &clientSide{t}
}

func (t *bidi) serverSide() GRPCStream {
	return &serverSide{t}
}

type clientSide struct {
	*bidi
}

func (c *clientSide) Recv() (*manager.TunnelMessage, error) {
	return c.sToC.recv()
}

func (c *clientSide) Send(msg *manager.TunnelMessage) error {
	return c.cToS.send(msg)
}

func (c *clientSide) CloseSend() error {
	return c.cToS.close()
}

type serverSide struct {
	*bidi
}

func (c *serverSide) Recv() (*manager.TunnelMessage, error) {
	return c.cToS.recv()
}

func (c *serverSide) Send(msg *manager.TunnelMessage) error {
	return c.sToC.send(msg)
}

func testContext(t *testing.T, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(dlog.WithLogger(context.Background(), log.NewTestLogger(t, dlog.LogLevelDebug)), timeout)
}

func TestStream_Connect(t *testing.T) {
	ctx, cancel := testContext(t, time.Second)
	defer cancel()

	tunnel := newBidi(10, ctx.Done())
	id := NewConnID(ipproto.TCP, iputil.Parse("127.0.0.1"), iputil.Parse("192.168.0.1"), 1001, 8080)
	si := uuid.New().String()

	wg := sync.WaitGroup{}
	wg.Add(2)
	go func() {
		defer wg.Done()
		client, err := NewClientStream(ctx, tunnel.clientSide(), id, si, 0, 0)
		require.NoError(t, err)
		assert.Equal(t, Version, client.PeerVersion())
		assert.NoError(t, client.CloseSend(ctx))
	}()

	go func() {
		defer wg.Done()
		server, err := NewServerStream(ctx, tunnel.serverSide())
		require.NoError(t, err)
		assert.Equal(t, id, server.ID())
		assert.Equal(t, Version, server.PeerVersion())
		assert.Equal(t, si, server.SessionID())
	}()
	wg.Wait()
}

func produce(ctx context.Context, s Stream, msg Message, errs chan<- error) {
	wrCh := make(chan Message)
	wg := sync.WaitGroup{}
	wg.Add(1)
	WriteLoop(ctx, s, wrCh, &wg, nil)
	go func() {
		for i := 0; i < 100; i++ {
			wrCh <- msg
		}
		close(wrCh)
		wg.Wait()
	}()

	rdCh, errCh := ReadLoop(ctx, s, nil)
	select {
	case <-ctx.Done():
		errs <- ctx.Err()
	case err, ok := <-errCh:
		if ok {
			errs <- err
		}
	case m, ok := <-rdCh:
		if ok {
			errs <- fmt.Errorf("unexpected message: %s", m)
		}
	}
}

func consume(ctx context.Context, s Stream, expectedPayload []byte, errs chan<- error) {
	count := 0
	wrCh := make(chan Message)
	wg := sync.WaitGroup{}
	wg.Add(1)
	WriteLoop(ctx, s, wrCh, &wg, nil)
	defer close(wrCh)
	rdCh, errCh := ReadLoop(ctx, s, nil)
	for {
		select {
		case <-ctx.Done():
			errs <- ctx.Err()
		case err, ok := <-errCh:
			if ok {
				errs <- err
			}
		case m, ok := <-rdCh:
			if !ok {
				return
			}
			if m.Code() != Normal {
				errs <- fmt.Errorf("unexpected message code %s", m.Code())
				return
			}
			if !bytes.Equal(expectedPayload, m.Payload()) {
				errs <- errors.New("unexpected message content")
				return
			}
			count++
		}
	}
}

func requireNoErrs(t *testing.T, errs chan error) chan error {
	t.Helper()
	close(errs)
	for err := range errs {
		assert.NoError(t, err)
	}
	if t.Failed() {
		t.FailNow()
	}
	return make(chan error, 10)
}

func TestStream_Xfer(t *testing.T) {
	ctx, cancel := testContext(t, 30*time.Second)
	defer cancel()

	id := NewConnID(ipproto.TCP, iputil.Parse("127.0.0.1"), iputil.Parse("192.168.0.1"), 1001, 8080)
	si := uuid.New().String()
	b := make([]byte, 0x1000)
	for i := range b {
		b[i] = byte(i & 0xff)
	}
	large := NewMessage(Normal, b)
	errs := make(chan error, 10)

	// Send data from client to server
	t.Run("client to server", func(t *testing.T) {
		tunnel := newBidi(10, ctx.Done())
		wg := sync.WaitGroup{}
		wg.Add(2)
		go func() {
			defer wg.Done()
			if client, err := NewClientStream(ctx, tunnel.clientSide(), id, si, 0, 0); err != nil {
				errs <- err
			} else {
				produce(ctx, client, large, errs)
			}
		}()
		go func() {
			defer wg.Done()
			if server, err := NewServerStream(ctx, tunnel.serverSide()); err != nil {
				errs <- err
			} else {
				consume(ctx, server, b, errs)
			}
		}()
		wg.Wait()
		errs = requireNoErrs(t, errs)
	})

	t.Run("server to client", func(t *testing.T) {
		tunnel := newBidi(10, ctx.Done())
		wg := sync.WaitGroup{}
		wg.Add(2)
		go func() {
			defer wg.Done()
			if server, err := NewServerStream(ctx, tunnel.serverSide()); err != nil {
				errs <- err
			} else {
				produce(ctx, server, large, errs)
			}
		}()
		go func() {
			defer wg.Done()
			if client, err := NewClientStream(ctx, tunnel.clientSide(), id, si, 0, 0); err != nil {
				errs <- err
			} else {
				consume(ctx, client, b, errs)
			}
		}()
		wg.Wait()
		errs = requireNoErrs(t, errs)
	})

	t.Run("client to client over BidiPipe", func(t *testing.T) {
		ta := newBidi(10, ctx.Done())
		tb := newBidi(10, ctx.Done())

		var counter int32
		aCh := make(chan Stream)
		bCh := make(chan Stream)
		wg := sync.WaitGroup{}
		wg.Add(5)
		go func() {
			defer wg.Done()
			if s, err := NewServerStream(ctx, ta.serverSide()); err != nil {
				errs <- err
				close(aCh)
			} else {
				aCh <- s
			}
		}()
		go func() {
			defer wg.Done()
			if s, err := NewServerStream(ctx, tb.serverSide()); err != nil {
				errs <- err
				close(bCh)
			} else {
				bCh <- s
			}
		}()
		go func() {
			defer wg.Done()
			if server, err := NewClientStream(ctx, ta.clientSide(), id, si, 0, 0); err != nil {
				errs <- err
			} else {
				produce(ctx, server, large, errs)
			}
		}()
		go func() {
			defer wg.Done()
			if client, err := NewClientStream(ctx, tb.clientSide(), id, si, 0, 0); err != nil {
				errs <- err
			} else {
				consume(ctx, client, b, errs)
			}
		}()
		go func() {
			defer wg.Done()
			var a, b Stream
			for a == nil || b == nil {
				select {
				case <-ctx.Done():
					errs <- ctx.Err()
					return
				case a = <-aCh:
				case b = <-bCh:
				}
			}
			fwd := NewBidiPipe(a, b, "pipe", &counter, nil)
			fwd.Start(ctx)
			select {
			case <-ctx.Done():
				errs <- ctx.Err()
			case <-fwd.Done():
			}
		}()
		wg.Wait()
		errs = requireNoErrs(t, errs)
	})
}
