package dnet_test

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"

	"golang.org/x/net/nettest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/dnet"
)

type mockManager struct {
	manager.UnsafeManagerProxyServer
	handleConn func(manager.ManagerProxy_HandleConnectionServer) error
}

func (mm *mockManager) HandleConnection(obj manager.ManagerProxy_HandleConnectionServer) error {
	return mm.handleConn(obj)
}

func TestHandledAmbassadorCloudConnection(t *testing.T) {
	makePipe := func() (c1, c2 net.Conn, stop func(), err error) {
		ctx, cancel := context.WithCancel(dcontext.WithSoftness(dlog.NewTestContext(t, false)))
		var wg sync.WaitGroup
		stop = func() {
			cancel()
			wg.Wait()
		}

		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, nil, nil, err
		}
		rawClient, err := grpc.DialContext(ctx,
			fmt.Sprintf("127.0.0.1:%d", listener.Addr().(*net.TCPAddr).Port),
			grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return nil, nil, nil, err
		}

		srvConnCh := make(chan net.Conn)
		wg.Add(1)
		go func() {
			defer wg.Done()
			handler := grpc.NewServer()
			manager.RegisterManagerProxyServer(handler, &mockManager{
				handleConn: func(srvObj manager.ManagerProxy_HandleConnectionServer) error {
					closed := make(chan struct{})
					id, conn, err := dnet.AcceptFromAmbassadorCloud(srvObj, func() {
						close(closed)
					})
					if err != nil {
						return err
					}
					t.Logf("accepted connection for interceptID=%q", id)
					srvConnCh <- conn
					<-closed
					return nil
				},
			})
			sc := dhttp.ServerConfig{
				Handler: handler,
			}
			if err := sc.Serve(ctx, listener); err != nil {
				t.Error(err)
			}
		}()

		client := manager.NewManagerProxyClient(rawClient)
		cliConn, err := dnet.DialFromAmbassadorCloud(ctx, client, "bogus-intercept-id")
		if err != nil {
			stop()
			return nil, nil, nil, err
		}

		return cliConn, <-srvConnCh, stop, nil
	}
	t.Run("Client", func(t *testing.T) { nettest.TestConn(t, makePipe) })
	t.Run("Server", func(t *testing.T) { nettest.TestConn(t, flipMakePipe(makePipe)) })
}
