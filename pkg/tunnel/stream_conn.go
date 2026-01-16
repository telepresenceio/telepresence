package tunnel

import (
	"context"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// NewStreamConn returns a net.Conn that reads and writes messages from the given stream.
// The read and write probes are optional.
func NewStreamConn(ctx context.Context, s Stream, readProbe, writeProbe *CounterProbe) net.Conn {
	return &streamConn{
		ctx:           ctx,
		stream:        s,
		readProbe:     readProbe,
		writeProbe:    writeProbe,
		readCancelCh:  make(chan struct{}),
		writeCancelCh: make(chan struct{}),
	}
}

type streamConn struct {
	ctx           context.Context
	stream        Stream
	readCancelCh  chan struct{}
	writeCancelCh chan struct{}
	readProbe     *CounterProbe
	writeProbe    *CounterProbe

	// The lastIncoming message and the offset into it are protected by readLock.
	readLock     sync.Mutex
	offset       int
	lastIncoming Message

	// Using atomic.LoadInt64/atomic.StoreInt64 to avoid locking
	readDeadline  int64
	writeDeadline int64
}

func (c *streamConn) readMore() (err error) {
	c.offset = 0
	ctx := c.ctx
	if rdl := atomic.LoadInt64(&c.readDeadline); rdl != 0 {
		dl := time.Unix(0, rdl)
		if dl.Before(time.Now()) {
			return context.DeadlineExceeded
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, dl)
		defer cancel()
	}

	// We must have a separate goroutine to do the read because cancelling the context is not respected by the grpc Recv function.
	type msgAndErr struct {
		msg Message
		err error
	}
	msgCh := make(chan msgAndErr, 1)
	go func() {
		var me msgAndErr
		me.msg, me.err = c.stream.Receive(ctx)
		msgCh <- me
	}()
	select {
	case <-ctx.Done():
		err = ctx.Err()
	case <-c.readCancelCh:
		err = context.DeadlineExceeded
	case me := <-msgCh:
		c.lastIncoming = me.msg
		err = me.err
	}
	if err != nil && strings.Contains(err.Error(), "use of closed network connection") {
		err = io.EOF
	}
	return err
}

func (c *streamConn) Read(data []byte) (bytesRead int, err error) {
	bytesRead = 0
	c.readLock.Lock()
	defer func() {
		if c.readProbe != nil && bytesRead > 0 {
			c.readProbe.Increment(uint64(bytesRead))
		}
		c.readLock.Unlock()
	}()

	if c.lastIncoming == nil {
		err = c.readMore()
		if err != nil {
			return bytesRead, err
		}
	}
	pl := c.lastIncoming.Payload()
	if len(pl)-c.offset <= 0 {
		err = c.readMore()
		if err != nil {
			return bytesRead, err
		}
		pl = c.lastIncoming.Payload()
	}
	bytesCopied := copy(data[bytesRead:], pl[c.offset:])
	c.offset += bytesCopied
	bytesRead += bytesCopied
	if c.offset == len(pl) {
		c.lastIncoming = nil
	}
	return bytesRead, err
}

func (c *streamConn) Write(b []byte) (n int, err error) {
	ctx := c.ctx
	if wdl := atomic.LoadInt64(&c.writeDeadline); wdl != 0 {
		dl := time.Unix(0, wdl)
		if dl.Before(time.Now()) {
			return 0, context.DeadlineExceeded
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, dl)
		defer cancel()
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- c.stream.Send(ctx, NewMessage(Normal, b))
	}()
	select {
	case <-ctx.Done():
		err = ctx.Err()
	case <-c.writeCancelCh:
		err = context.DeadlineExceeded
	case err = <-errCh:
	}
	if err != nil {
		return 0, err
	}
	n = len(b)
	if c.writeProbe != nil {
		c.writeProbe.Increment(uint64(n))
	}
	clog.Debugf(c.ctx, "Write %d bytes", n)
	return n, nil
}

func (c *streamConn) Close() error {
	select {
	case <-c.readCancelCh:
		// Already closed.
		return nil
	default:
	}
	close(c.readCancelCh)
	close(c.writeCancelCh)
	return c.stream.CloseSend(c.ctx)
}

func addFromAP(ap netip.AddrPort, proto types.Proto) net.Addr {
	if proto == types.ProtoUDP {
		return net.UDPAddrFromAddrPort(ap)
	}
	return net.TCPAddrFromAddrPort(ap)
}

func (c *streamConn) LocalAddr() net.Addr {
	id := c.stream.ID()
	return addFromAP(id.Source(), id.Protocol())
}

func (c *streamConn) RemoteAddr() net.Addr {
	id := c.stream.ID()
	return addFromAP(id.Destination(), id.Protocol())
}

func (c *streamConn) SetDeadline(t time.Time) error {
	err := c.SetReadDeadline(t)
	if err == nil {
		err = c.SetWriteDeadline(t)
	}
	return err
}

func (c *streamConn) SetReadDeadline(t time.Time) error {
	var un int64 = 0
	if !t.IsZero() {
		un = t.UnixNano()
	}
	atomic.StoreInt64(&c.readDeadline, un)
	select {
	case c.readCancelCh <- struct{}{}:
	default:
	}
	return nil
}

func (c *streamConn) SetWriteDeadline(t time.Time) error {
	var un int64 = 0
	if !t.IsZero() {
		un = t.UnixNano()
	}
	atomic.StoreInt64(&c.writeDeadline, un)
	select {
	case c.writeCancelCh <- struct{}{}:
	default:
	}
	return nil
}
