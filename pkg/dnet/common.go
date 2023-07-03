// Package dnet contains alternative net.Conn implementations.
package dnet

import (
	"bytes"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Conn is a net.Conn, plus a Wait() method.
type Conn interface {
	net.Conn

	// Wait until either the connection is Close()d, or one of Read() or Write() encounters an
	// error (*not* counting errors caused by deadlines).  If this returns because Close() was
	// called, nil is returned; otherwise the triggering error is returned.
	//
	// Essentially: Wait until the connection is finished.
	Wait() error
}

type Addr struct {
	Net  string
	Addr string
}

func (a Addr) Network() string { return a.Net }
func (a Addr) String() string  { return a.Addr }

// UnbufferedConn represents a reliable fully-synchronous stream with *no* internal buffering.  But
// really, what it is is "everything that isn't generic enough to be in bufferedConn".
type UnbufferedConn interface {
	// Receive some data over the connection.
	Recv() ([]byte, error)

	// Send some data over the connection.  Because the connection is fully-synchronous and has
	// no internal buffering, Send must not return until the remote end acknowledges the full
	// transmission (or an error is encountered).
	Send([]byte) error

	// MTU returns the largest amount of data that is permissible to include in a single Send
	// call.
	MTU() int

	// CloseOnce closes both the read-end and write-end of the connection.  Any blocked Recv or
	// Send operations will be unblocked and return errors.  It is an error to call CloseOnce
	// more than once.
	CloseOnce() error

	// LocalAddr returns the local network address.
	LocalAddr() net.Addr

	// RemoteAddr returns the remote network address.
	RemoteAddr() net.Addr
}

// bufferedConn is a net.Conn implementation that uses a reliable fully-synchronous stream as the
// underlying transport.
//
// This at first appears more complex than it needs to be: Why have buffers and pump goroutines,
// instead of simply synchronously calling .Send and .Recv on the underlying stream?  stdlib
// net.TCPConn gets away with that for synchronously reading and writing the underlying file
// descriptor, so why can't we get away this the same simplicity?  Because the OS kernel is doing
// that same buffering and pumping for TCP; Go stdlib doesn't have to do it because it's happening
// in kernel space.  But since we don't have a raw FD that the kernel can do things with, we have to
// do that those things in userspace.
type bufferedConn struct {
	// configuration

	conn UnbufferedConn

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

func WrapUnbufferedConn(inner UnbufferedConn) Conn {
	c := &bufferedConn{
		conn: inner,

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

func (c *bufferedConn) isClosed() bool {
	return atomic.LoadInt32(&c.closed) != 0
}

func (c *bufferedConn) readPump() {
	defer close(c.readDone)

	keepGoing := true
	// use isClosedPipe(c.writeDone) instead of c.isClosed() to keep the readPump running just a
	// little longer, in case the other end acking our writes is blocking on us acking their
	// writes.
	for keepGoing && !isClosedChan(c.writeDone) {
		data, err := c.conn.Recv()

		c.readCond.L.Lock()
		if len(data) > 0 {
			c.readBuff.Write(data)
		}
		if err != nil {
			c.readErr = err
			keepGoing = false
		}
		c.readCond.L.Unlock()

		if len(data) > 0 || err != nil {
			// .Broadcast() instead of .Signal() in case there are multiple waiting
			// readers that are each asking for less than len(chunk.Content) bytes.
			c.readCond.Broadcast()
		}
	}
}

// Read implements net.Conn.
func (c *bufferedConn) Read(b []byte) (int, error) {
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

// must hold c.readCond.Mu to call readReset.
func (c *bufferedConn) readReset() {
	c.readBuff.Reset()
	// This isn't so much to notify readers of the readBuff.Reset(), but of *whatever caused it*.
	c.readCond.Broadcast()
}

// Write implements net.Conn.
func (c *bufferedConn) Write(b []byte) (int, error) {
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

// must hold c.writeCond.Mu to call writeReset.
func (c *bufferedConn) writeReset() {
	c.writeBuff.Reset()
	// Don't bother with c.writeCond.Broadcast() because this will only ever make the condition
	// false.
}

func (c *bufferedConn) writePump() {
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
		if mtu := c.conn.MTU(); mtu > 0 && tu > mtu {
			tu = mtu
		}
		if len(buff) < tu {
			buff = make([]byte, tu)
		}
		n, _ := c.writeBuff.Read(buff[:tu])
		c.writeCond.L.Unlock()

		// Write the data.
		if err := c.conn.Send(buff[:n]); err != nil {
			c.writeCond.L.Lock()
			c.writeErr = err
			c.writeCond.L.Unlock()
			return
		}
	}
}

// Close implements net.Conn.  Both the read-end and the write-end are closed.  Any blocked Read or
// Write operations will be unblocked and return errors.
func (c *bufferedConn) Close() error {
	c.closeOnce.Do(func() {
		atomic.StoreInt32(&c.closed, 1)

		// Don't c.writeReset(), let the write buffer drain normally; otherwise the user has
		// no way of ensuring that the write went through.  This is consistent with close(2)
		// semantics on most unixes.
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

		// readPump might be blocked on c.conn.Recv(), so we might need to force it closed
		// to interrupt that.
		c.writeCond.L.Lock()
		c.closeErr = c.conn.CloseOnce()
		c.writeCond.L.Unlock()

		// Wait for readPump to drain (triggered above).
		<-c.readDone
	})
	return c.closeErr
}

// LocalAddr implements net.Conn.
func (c *bufferedConn) LocalAddr() net.Addr {
	return c.conn.LocalAddr()
}

// RemoteAddr implements net.Conn.
func (c *bufferedConn) RemoteAddr() net.Addr {
	return c.conn.RemoteAddr()
}

// SetDeadline implements net.Conn.
func (c *bufferedConn) SetDeadline(t time.Time) error {
	if c.isClosed() {
		return os.ErrClosed
	}
	c.readDeadline.set(t)
	c.writeDeadline.set(t)
	return nil
}

// SetReadDeadline implements net.Conn.
func (c *bufferedConn) SetReadDeadline(t time.Time) error {
	if c.isClosed() {
		return os.ErrClosed
	}
	c.readDeadline.set(t)
	return nil
}

// SetWriteDeadline implements net.Conn.
func (c *bufferedConn) SetWriteDeadline(t time.Time) error {
	if c.isClosed() {
		return os.ErrClosed
	}
	c.writeDeadline.set(t)
	return nil
}

// Wait implements dnet.Conn.
func (c *bufferedConn) Wait() error {
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

func isClosedChan(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}
