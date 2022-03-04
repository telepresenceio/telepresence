package dnet

import (
	"context"
	"errors"
	"fmt"
	"net"

	"google.golang.org/grpc"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// handledConnectionImpl is the intersection of these two interfaces:
//
//   manager.ManagerProxy_HandleConnectionClient
//   manager.ManagerProxy_HandleConnectionServer
type handledConnectionImpl interface {
	Send(*manager.ConnectionChunk) error
	Recv() (*manager.ConnectionChunk, error)
}

// DialFromAmbassadorCloud is used by Ambassador Cloud to dial to the manager, initiating a
// connection to an intercept for a preview URL.
//
// It is the counterpart to AcceptFromAmbassadorCloud.
func DialFromAmbassadorCloud(ctx context.Context, managerClient manager.ManagerProxyClient, interceptID string, opts ...grpc.CallOption) (Conn, error) {
	impl, err := managerClient.HandleConnection(ctx, opts...)
	if err != nil {
		return nil, err
	}
	err = impl.Send(&manager.ConnectionChunk{
		Value: &manager.ConnectionChunk_InterceptId{
			InterceptId: interceptID,
		},
	})
	if err != nil {
		_ = impl.CloseSend()
		return nil, err
	}
	return wrapUnbufferedConn(handledConn{conn: impl}), nil
}

// AcceptFromAmbassadorCloud is used by a Telepresence manger to accept a connection to an intercept
// for a preview URL from Ambassador Cloud.
//
// It is the counterpart to DialFromAmbassadorCloud.
func AcceptFromAmbassadorCloud(systema manager.ManagerProxy_HandleConnectionServer, closeFn func()) (interceptID string, conn Conn, err error) {
	chunk, err := systema.Recv()
	if err != nil {
		return "", nil, err
	}
	chunkValue, ok := chunk.Value.(*manager.ConnectionChunk_InterceptId)
	if !ok {
		return "", nil, fmt.Errorf("HandleConnection: first chunk must be an intercept_id")
	}
	interceptID = chunkValue.InterceptId

	return interceptID, wrapUnbufferedConn(handledConn{conn: systema, close: closeFn}), nil
}

// handledConn is an unbufferedConn implementation that uses a gRPC
// "/telepresence.manager/ManagerProxy/HandleConnection" stream as the underlying transport.
type handledConn struct {
	conn  handledConnectionImpl
	close func()
}

// Recv implements unbufferedConn.
func (c handledConn) Recv() ([]byte, error) {
	chunk, err := c.conn.Recv()

	var data []byte
	if chunk != nil && chunk.Value != nil {
		switch chunk := chunk.Value.(type) {
		case *manager.ConnectionChunk_InterceptId:
			if err == nil {
				err = errors.New("HandleConnection: unexpected intercept_id chunk")
			}
		case *manager.ConnectionChunk_Data:
			if chunk != nil && len(chunk.Data) > 0 {
				data = chunk.Data
			}
		case *manager.ConnectionChunk_Error:
			if err == nil {
				err = fmt.Errorf("HandleConnection: remote error: %s", chunk.Error)
			}
		}
	}

	return data, err
}

// Send implements unbufferedConn.
func (c handledConn) Send(data []byte) error {
	return c.conn.Send(&manager.ConnectionChunk{
		Value: &manager.ConnectionChunk_Data{
			Data: data,
		},
	})
}

// CloseOnce implements unbufferedConn.
func (c handledConn) CloseOnce() error {
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
func (c handledConn) MTU() int {
	// 3MiB; assume the other end's gRPC library uses the go-grpc default 4MiB, plus plenty of
	// room for overhead.
	return 3 * 1024 * 1024
}

// LocalAddr implements unbufferedConn.
func (c handledConn) LocalAddr() net.Addr {
	_, isClient := c.conn.(grpc.ClientStream)
	if isClient {
		return addr{
			net:  "tp-handleconnection",
			addr: "localrole=client,localhostname=acloud",
		}
	} else {
		return addr{
			net:  "tp-handleconnection",
			addr: "localrole=server,localhostname=manager",
		}
	}
}

// RemoteAddr implements unbufferedConn.
func (c handledConn) RemoteAddr() net.Addr {
	_, isClient := c.conn.(grpc.ClientStream)
	if isClient {
		return addr{
			net:  "tp-handleconnection",
			addr: "localrole=client,remotehostname=manager",
		}
	} else {
		return addr{
			net:  "tp-handleconnection",
			addr: "localrole=client,remotehostname=acloud",
		}
	}
}
