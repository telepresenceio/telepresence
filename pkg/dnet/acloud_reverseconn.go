package dnet

import (
	"bytes"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"

	"github.com/telepresenceio/telepresence/rpc/v2/systema"
)

// ambassadorCloudTunnel is the intersection of these two interfaces:
//
//   systema.SystemAProxy_ReverseConnectionClient
//   systema.SystemAProxy_ReverseConnectionServer
type ambassadorCloudTunnel interface {
	Send(*systema.Chunk) error
	Recv() (*systema.Chunk, error)
}

// reverseConn is a net.Conn implementation that uses a gRPC
// "/telepresence.systema/SystemAProxy/ReverseConnection" stream as the underlying transport.
//
// This at first appears more complex than it needs to be: Why have buffers and pump goroutines,
// instead of simply synchronously calling .Send and .Recv on the gRPC stream?  stdlib net.TCPConn
// gets away with that for synchronously reading and writing the underlying file descriptor, so why
// can't we we get away this the same simplicity?  Because the OS kernel is doing that same
// buffering and pumping for TCP; Go stdlib doesn't have to do it because it's happening in kernel
// space.  But since we don't have a raw FD that the kernel can do things with, we have to do that
// those things in userspace.
type reverseConn struct {
	// configuration

	conn  ambassadorCloudTunnel
	close func()
	mtu   int

	// state

	closeOnce sync.Once
	closed    int32 // atomic
	closeErr  error

	readCond     sync.Cond
	readBuff     bytes.Buffer // must hold readCond.L to access
	readErr      error        // must hold readCond.L to access
	readDone     chan struct{}
	readDeadline atomicDeadline

	writeCond     sync.Cond
	writeBuff     bytes.Buffer // must hold writeCond.L to access
	writeErr      error        // must hold writeCond.L to access
	writeDone     chan struct{}
	writeDeadline atomicDeadline
}

// WrapAmbassadorCloudTunnelClient takes a systema.SystemAProxy_ReverseConnectionClient and wraps it
// so that it can be used as a net.Conn.
//
// It is important to call `.Close()` when you are done with the connection, in order to release
// resources associated with it.  The GC will not be able to collect it if you do not call
// `.Close()`.
func WrapAmbassadorCloudTunnelClient(impl systema.SystemAProxy_ReverseConnectionClient) Conn {
	return wrapAmbassadorCloudTunnel(impl, nil)
}

// WrapAmbassadorCloudTunnel takes a systema.SystemAProxy_ReverseConnectionServer and wraps it so
// that it can be used as a net.Conn.
//
// It is important to call `.Close()` when you are done with the connection, in order to release
// resources associated with it.  The GC will not be able to collect it if you do not call
// `.Close()`.
func WrapAmbassadorCloudTunnelServer(impl systema.SystemAProxy_ReverseConnectionServer, closeFn func()) net.Conn {
	return wrapAmbassadorCloudTunnel(impl, closeFn)
}

func wrapAmbassadorCloudTunnel(impl ambassadorCloudTunnel, closeFn func()) Conn {
	c := &reverseConn{
		conn:  impl,
		close: closeFn,
		// 3MiB; assume the other end's gRPC library uses the go-grpc
		// default 4MiB, plus plenty of room for overhead.
		mtu: 3 * 1024 * 1024,

		readDone:  make(chan struct{}),
		writeDone: make(chan struct{}),

		readCond:  sync.Cond{L: &sync.Mutex{}},
		writeCond: sync.Cond{L: &sync.Mutex{}},
	}

	c.readDeadline = atomicDeadline{
		cbMu: c.readCond.L,
		cb:   c.readReset,
	}
	go c.readPump()

	c.writeDeadline = atomicDeadline{
		cbMu: c.writeCond.L,
		cb:   c.writeReset,
	}
	go c.writePump()

	return c
}

func (c *reverseConn) isClosed() bool {
	return atomic.LoadInt32(&c.closed) != 0
}

func (c *reverseConn) readPump() {
	defer close(c.readDone)

	keepGoing := true
	// use isClosedPipe(c.writeDone) instead of c.isClosed() to keep the readPump running just a
	// little longer, in case the other end acking our writes is blocking on us acking their
	// writes.
	for keepGoing && !isClosedChan(c.writeDone) {
		chunk, err := c.conn.Recv()

		c.readCond.L.Lock()
		if chunk != nil && len(chunk.Content) > 0 {
			c.readBuff.Write(chunk.Content)
		}
		if err != nil {
			c.readErr = err
			keepGoing = false
		}
		c.readCond.L.Unlock()

		if (chunk != nil && len(chunk.Content) > 0) || err != nil {
			// .Broadcast() instead of .Signal() in case there are multiple waiting
			// readers that are each asking for less than len(chunk.Content) bytes.
			c.readCond.Broadcast()
		}
	}
}

// Read implements net.Conn.
func (c *reverseConn) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}

	c.readCond.L.Lock()
	defer c.readCond.L.Unlock()

	for c.readBuff.Len() == 0 {
		switch {
		case c.readErr != nil:
			return 0, c.readErr
		case c.isClosed():
			return 0, os.ErrClosed
		case c.readDeadline.isCanceled():
			return 0, os.ErrDeadlineExceeded
		}
		c.readCond.Wait()
	}

	return c.readBuff.Read(b)
}

// must hold c.readCond.Mu to call readReset
func (c *reverseConn) readReset() {
	c.readBuff.Reset()
	// This isn't so much to notify readers of the readBuff.Reset(), but of *whatever caused it*.
	c.readCond.Broadcast()
}

// Write implements net.Conn.
func (c *reverseConn) Write(b []byte) (int, error) {
	c.writeCond.L.Lock()
	defer c.writeCond.L.Unlock()

	switch {
	case c.writeErr != nil:
		return 0, c.writeErr
	case c.isClosed():
		return 0, os.ErrClosed
	case c.writeDeadline.isCanceled():
		return 0, os.ErrDeadlineExceeded
	}

	n, err := c.writeBuff.Write(b)
	if n > 0 {
		// The only reader is the singular writePump, so don't bother with .Broadcast() when
		// .Signal() is fine.
		c.writeCond.Signal()
	}
	return n, err
}

// must hold c.writeCond.Mu to call writeReset
func (c *reverseConn) writeReset() {
	c.writeBuff.Reset()
	// Don't bother with c.writeCond.Broadcast() because this will only ever make the condition
	// false.
}

func (c *reverseConn) writePump() {
	defer close(c.writeDone)

	var buff []byte

	for {
		// Get the data to write.
		c.writeCond.L.Lock()
		for c.writeBuff.Len() == 0 && !c.isClosed() {
			c.writeCond.Wait()
		}
		if c.writeBuff.Len() == 0 {
			// closed
			c.writeCond.L.Unlock()
			return
		}
		tu := c.writeBuff.Len() // "transmission unit", as in "MTU"
		if tu > c.mtu {
			tu = c.mtu
		}
		if len(buff) < tu {
			buff = make([]byte, tu)
		}
		n, _ := c.writeBuff.Read(buff[:tu])
		c.writeCond.L.Unlock()

		// Write the data.
		err := c.conn.Send(&systema.Chunk{
			Content: buff[:n],
		})
		if err != nil {
			c.writeCond.L.Lock()
			c.writeErr = err
			c.writeCond.L.Unlock()
			return
		}
	}
}

// Close implements net.Conn.  Both the read-end and the write-end are closed.  Any blocked Read or
// Write operations will be unblocked and return errors.
func (c *reverseConn) Close() error {
	c.closeOnce.Do(func() {
		atomic.StoreInt32(&c.closed, 1)

		// Don't c.writeReset(), let the write buffer drain normally; otherwise the user has
		// no way of ensuring that the write went through.
		c.writeCond.Signal() // if writePump is blocked, notify it of the change to c.closed

		// OTOH: c.readReset().  Unlike the write buffer, we do forcefully reset this
		// instead of letting it drain normally.  If you close something you're reading
		// before you've received EOF, you are liable to lose data; this is no different.
		// It's only happenstance that we even have that data in our buffer; it might as
		// well still be in transit on a slow wire.
		c.readCond.L.Lock()
		c.readReset()
		c.readCond.L.Unlock()

		// Wait for writePump to drain (triggered above).
		<-c.writeDone

		// readPump might be blocked on c.conn.Recv(), so we might need to use force it
		// closed to interrupt that.
		if client, isClient := c.conn.(grpc.ClientStream); isClient {
			c.writeCond.L.Lock()
			c.closeErr = client.CloseSend()
			c.writeCond.L.Unlock()
		}
		if c.close != nil {
			c.close()
		}

		// Wait for readPump to drain (triggered above).
		<-c.readDone
	})
	return c.closeErr
}

// LocalAddr implements net.Conn.
func (c *reverseConn) LocalAddr() net.Addr {
	_, isClient := c.conn.(grpc.ClientStream)
	if isClient {
		return addr{
			net:  "tp-reverseconnection",
			addr: "localrole=client,localhostname=manager",
		}
	} else {
		return addr{
			net:  "tp-reverseconnection",
			addr: "localrole=server,localhostname=acloud",
		}
	}
}

// RemoteAddr implements net.Conn.
func (c *reverseConn) RemoteAddr() net.Addr {
	_, isClient := c.conn.(grpc.ClientStream)
	if isClient {
		return addr{
			net:  "tp-reverseconnection",
			addr: "localrole=client,remotehostname=acloud",
		}
	} else {
		return addr{
			net:  "tp-reverseconnection",
			addr: "localrole=server,remotehostname=manager",
		}
	}
}

// SetDeadline implements net.Conn.
func (c *reverseConn) SetDeadline(t time.Time) error {
	if c.isClosed() {
		return os.ErrClosed
	}
	c.readDeadline.set(t)
	c.writeDeadline.set(t)
	return nil
}

// SetReadDeadline implements net.Conn.
func (c *reverseConn) SetReadDeadline(t time.Time) error {
	if c.isClosed() {
		return os.ErrClosed
	}
	c.readDeadline.set(t)
	return nil
}

// SetWriteDeadline implements net.Conn.
func (c *reverseConn) SetWriteDeadline(t time.Time) error {
	if c.isClosed() {
		return os.ErrClosed
	}
	c.writeDeadline.set(t)
	return nil
}

// Wait implements dnet.Conn
func (c *reverseConn) Wait() error {
	<-c.readDone
	if c.readErr != nil {
		return c.readErr
	}
	if c.writeErr != nil {
		return c.writeErr
	}
	if c.closeErr != nil {
		return c.closeErr
	}
	return nil
}
