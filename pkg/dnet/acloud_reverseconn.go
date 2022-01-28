package dnet

import (
	"net"

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

// reverseConn is an unbufferedConn implementation that uses a gRPC
// "/telepresence.systema/SystemAProxy/ReverseConnection" stream as the underlying transport.
//
type reverseConn struct {
	conn  ambassadorCloudTunnel
	close func()
}

// WrapAmbassadorCloudTunnelClient takes a systema.SystemAProxy_ReverseConnectionClient and wraps it
// so that it can be used as a net.Conn.
//
// It is important to call `.Close()` when you are done with the connection, in order to release
// resources associated with it.  The GC will not be able to collect it if you do not call
// `.Close()`.
func WrapAmbassadorCloudTunnelClient(impl systema.SystemAProxy_ReverseConnectionClient) Conn {
	return wrapUnbufferedConn(reverseConn{conn: impl})
}

// WrapAmbassadorCloudTunnel takes a systema.SystemAProxy_ReverseConnectionServer and wraps it so
// that it can be used as a net.Conn.
//
// It is important to call `.Close()` when you are done with the connection, in order to release
// resources associated with it.  The GC will not be able to collect it if you do not call
// `.Close()`.
func WrapAmbassadorCloudTunnelServer(impl systema.SystemAProxy_ReverseConnectionServer, closeFn func()) Conn {
	return wrapUnbufferedConn(reverseConn{conn: impl, close: closeFn})
}

// Recv implements unbufferedConn.
func (c reverseConn) Recv() ([]byte, error) {
	chunk, err := c.conn.Recv()

	var data []byte
	if chunk != nil && len(chunk.Content) > 0 {
		data = chunk.Content
	}

	return data, err
}

// Send implements unbufferedConn.
func (c reverseConn) Send(data []byte) error {
	return c.conn.Send(&systema.Chunk{
		Content: data,
	})
}

// CloseOnce implements unbufferedConn.
func (c reverseConn) CloseOnce() error {
	var err error
	if client, isClient := c.conn.(grpc.ClientStream); isClient {
		err = client.CloseSend()
	}
	if c.close != nil {
		c.close()
	}
	return err
}

// MTU implements unbufferedConn.
func (c reverseConn) MTU() int {
	// 3MiB; assume the other end's gRPC library uses the go-grpc default 4MiB, plus plenty of
	// room for overhead.
	return 3 * 1024 * 1024
}

// LocalAddr implements unbufferedConn.
func (c reverseConn) LocalAddr() net.Addr {
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

// RemoteAddr implements unbufferedConn.
func (c reverseConn) RemoteAddr() net.Addr {
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
