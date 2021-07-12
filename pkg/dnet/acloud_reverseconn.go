package dnet

import (
	"bytes"
	"net"
	"os"
	"sync"
	"time"

	"google.golang.org/grpc"

	"github.com/telepresenceio/telepresence/rpc/v2/systema"
)

// AmbassadorCloudTunnel is the intersection of these two interfaces:
//
//   systema.SystemAProxy_ReverseConnectionClient
//   systema.SystemAProxy_ReverseConnectionServer
type AmbassadorCloudTunnel interface {
	Send(*systema.Chunk) error
	Recv() (*systema.Chunk, error)
}

type reverseConn struct {
	conn AmbassadorCloudTunnel

	waitOnce sync.Once
	wait     chan struct{}
	waitErr  error

	closeOnce sync.Once
	closed    chan struct{}

	readMu       sync.Mutex
	readBuff     bytes.Buffer
	readDeadline pipeDeadline
	readErr      error

	writeMu       sync.Mutex
	writeDeadline pipeDeadline
	writeErr      error
}

// WrapAmbassadorCloudTunnel takes a systema.SystemAProxy_ReverseConnectionClient or
// systema.SystemAProxy_ReverseConnectionServer and wraps it so that it can be used as a net.Conn.
func WrapAmbassadorCloudTunnel(impl AmbassadorCloudTunnel) Conn {
	return &reverseConn{
		conn: impl,

		wait:          make(chan struct{}),
		closed:        make(chan struct{}),
		readDeadline:  makePipeDeadline(),
		writeDeadline: makePipeDeadline(),
	}
}

// Read implements net.Conn.
func (c *reverseConn) Read(b []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	for c.readBuff.Len() == 0 {
		if c.readErr == nil {
			switch {
			case isClosedChan(c.closed):
				c.readErr = os.ErrClosed
			case isClosedChan(c.readDeadline.wait()):
				c.readErr = os.ErrDeadlineExceeded
			}
		}
		if c.readErr != nil {
			return 0, c.readErr
		}

		chunk, err := c.conn.Recv()
		if chunk != nil && len(chunk.Content) > 0 {
			c.readBuff.Write(chunk.Content)
		}
		if err != nil && c.readErr == nil {
			c.waitOnce.Do(func() { c.waitErr = err; close(c.wait) })
			c.readErr = err
		}
	}
	return c.readBuff.Read(b)
}

// Write implements net.Conn.
func (c *reverseConn) Write(b []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	if c.writeErr == nil {
		switch {
		case isClosedChan(c.closed):
			c.writeErr = os.ErrClosed
		case isClosedChan(c.writeDeadline.wait()):
			c.writeErr = os.ErrDeadlineExceeded
		}
	}
	if c.writeErr != nil {
		return 0, c.writeErr
	}

	err := c.conn.Send(&systema.Chunk{
		Content: b,
	})
	if err != nil {
		c.waitOnce.Do(func() { c.waitErr = err; close(c.wait) })
		c.writeErr = err
		return 0, err
	}
	return len(b), nil
}

// Close implements net.Conn.
func (c *reverseConn) Close() error {
	c.closeOnce.Do(func() {
		if client, isClient := c.conn.(grpc.ClientStream); isClient {
			c.writeMu.Lock()
			defer c.writeMu.Unlock()
			_ = client.CloseSend()
		}
		c.waitOnce.Do(func() { close(c.wait) })
	})
	return nil
}

// LocalAddr implements net.Conn.
func (c *reverseConn) LocalAddr() net.Addr {
	_, isClient := c.conn.(grpc.ClientStream)
	if isClient {
		return addr{
			net:  "tp-reverseconnection-client",
			addr: "local",
		}
	} else {
		return addr{
			net:  "tp-reverseconnection-server",
			addr: "local",
		}
	}
}

// RemoteAddr implements net.Conn.
func (c *reverseConn) RemoteAddr() net.Addr {
	_, isClient := c.conn.(grpc.ClientStream)
	if isClient {
		return addr{
			net:  "tp-reverseconnection-client",
			addr: "remote",
		}
	} else {
		return addr{
			net:  "tp-reverseconnection-server",
			addr: "remote",
		}
	}
}

// SetDeadline implements net.Conn.
func (c *reverseConn) SetDeadline(t time.Time) error {
	if isClosedChan(c.closed) {
		return os.ErrClosed
	}
	c.readDeadline.set(t)
	c.writeDeadline.set(t)
	return nil
}

// SetReadDeadline implements net.Conn.
func (c *reverseConn) SetReadDeadline(t time.Time) error {
	if isClosedChan(c.closed) {
		return os.ErrClosed
	}
	c.readDeadline.set(t)
	return nil
}

// SetWriteDeadline implements net.Conn.
func (c *reverseConn) SetWriteDeadline(t time.Time) error {
	if isClosedChan(c.closed) {
		return os.ErrClosed
	}
	c.writeDeadline.set(t)
	return nil
}

// Wait waits until either the connection is Close()d, or one of Read() or Write() encounters an
// error (*not* counting errors caused by deadlines).  If this returns because Close() was called,
// nil is returned; otherwise the triggering error is returned.
func (c *reverseConn) Wait() error {
	<-c.wait
	return c.waitErr
}
