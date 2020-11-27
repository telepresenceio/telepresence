package systemaconn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"google.golang.org/grpc"

	"github.com/datawire/telepresence2/pkg/rpc/systema2manager"
)

type handledConnectionImpl interface {
	Send(*systema2manager.ConnectionChunk) error
	Recv() (*systema2manager.ConnectionChunk, error)
}

// DialToManager uses a ReverseConnection to dial from SystemA to a Telepresence manager.
func DialToManager(ctx context.Context, manager systema2manager.ManagerProxyClient, interceptID string, opts ...grpc.CallOption) (Conn, error) {
	impl, err := manager.HandleConnection(ctx, opts...)
	if err != nil {
		return nil, err
	}
	err = impl.Send(&systema2manager.ConnectionChunk{
		Value: &systema2manager.ConnectionChunk_InterceptId{
			InterceptId: interceptID,
		},
	})
	if err != nil {
		impl.CloseSend()
		return nil, err
	}
	return wrap(impl), nil
}

// AcceptFromSystemA is used by a Telepresence manger to accept a connection from SystemA.
func AcceptFromSystemA(systema systema2manager.ManagerProxy_HandleConnectionServer) (interceptID string, conn Conn, err error) {
	chunk, err := systema.Recv()
	if err != nil {
		return "", nil, err
	}
	chunkValue, ok := chunk.Value.(*systema2manager.ConnectionChunk_InterceptId)
	if !ok {
		return "", nil, fmt.Errorf("HandleConnection: first chunk must be an intercept_id")
	}
	interceptID = chunkValue.InterceptId

	return interceptID, wrap(systema), nil
}

type handledConn struct {
	conn handledConnectionImpl

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

// Wrap takes a systema2manager.ManagerProxy_HandleConnectionClient or
// systema2manager.ManagerProxy_HandleConnectionServer and wraps it so that it can be used as a
// net.Conn.
func wrap(impl handledConnectionImpl) Conn {
	return &handledConn{
		conn: impl,

		closed:        make(chan struct{}),
		readDeadline:  makePipeDeadline(),
		writeDeadline: makePipeDeadline(),
	}
}

// Read implements net.Conn.
func (c *handledConn) Read(b []byte) (int, error) {
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
		switch chunk := chunk.Value.(type) {
		case *systema2manager.ConnectionChunk_InterceptId:
			c.readErr = errors.New("HandleConnection: unexpected intercept_id chunk")
			c.Close()
			continue
		case *systema2manager.ConnectionChunk_Data:
			if chunk != nil && len(chunk.Data) > 0 {
				c.readBuff.Write(chunk.Data)
			}
		case *systema2manager.ConnectionChunk_Error:
			c.readErr = fmt.Errorf("HandleConnection: remote error: %s", chunk.Error)
			c.Close()
			continue
		}
		if err != nil && c.readErr == nil {
			c.waitOnce.Do(func() { c.waitErr = err; close(c.wait) })
			c.readErr = err
		}
	}
	return c.readBuff.Read(b)
}

// Write implements net.Conn.
func (c *handledConn) Write(b []byte) (int, error) {
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

	err := c.conn.Send(&systema2manager.ConnectionChunk{
		Value: &systema2manager.ConnectionChunk_Data{
			Data: b,
		},
	})
	if err != nil {
		c.waitOnce.Do(func() { c.waitErr = err; close(c.wait) })
		c.writeErr = err
		return 0, err
	}
	return len(b), nil
}

// Close implements net.Conn.
func (c *handledConn) Close() error {
	c.closeOnce.Do(func() {
		if client, isClient := c.conn.(grpc.ClientStream); isClient {
			c.writeMu.Lock()
			defer c.writeMu.Unlock()
			client.CloseSend()
		}
		c.waitOnce.Do(func() { close(c.wait) })
	})
	return nil
}

// LocalAddr implements net.Conn.
func (c *handledConn) LocalAddr() net.Addr {
	_, isClient := c.conn.(grpc.ClientStream)
	if isClient {
		return addr{
			net:  "tp-handleconnection-client",
			addr: "local",
		}
	} else {
		return addr{
			net:  "tp-handleconnection-server",
			addr: "local",
		}
	}
}

// RemoteAddr implements net.Conn.
func (c *handledConn) RemoteAddr() net.Addr {
	_, isClient := c.conn.(grpc.ClientStream)
	if isClient {
		return addr{
			net:  "tp-handleconnection-client",
			addr: "remote",
		}
	} else {
		return addr{
			net:  "tp-handleconnection-server",
			addr: "remote",
		}
	}
}

// SetDeadline implements net.Conn.
func (c *handledConn) SetDeadline(t time.Time) error {
	c.SetReadDeadline(t)
	c.SetWriteDeadline(t)
	return nil
}

// SetReadDeadline implements net.Conn.
func (c *handledConn) SetReadDeadline(t time.Time) error {
	if isClosedChan(c.closed) {
		c.readDeadline.set(t)
	}
	return nil
}

// SetWriteDeadline implements net.Conn.
func (c *handledConn) SetWriteDeadline(t time.Time) error {
	if isClosedChan(c.closed) {
		c.writeDeadline.set(t)
	}
	return nil
}

// Wait waits until either the connection is Close()d, or one of Read() or Write() encounters an
// error (*not* counting errors caused by deadlines).  If this returns because Close() was called,
// nil is returned; otherwise the triggering error is returned.
func (c *handledConn) Wait() error {
	<-c.wait
	return c.waitErr
}
