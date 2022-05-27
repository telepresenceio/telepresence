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
	"github.com/telepresenceio/telepresence/rpc/v2/systema"
	"github.com/telepresenceio/telepresence/v2/pkg/dnet"
)

type mockCloud struct {
	systema.UnsafeSystemAProxyServer
	handleConn func(systema.SystemAProxy_ReverseConnectionServer) error
}

func (mc *mockCloud) ReverseConnection(obj systema.SystemAProxy_ReverseConnectionServer) error {
	return mc.handleConn(obj)
}

func TestWrapAmbassadorCloudTunnel(t *testing.T) {
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
			systema.RegisterSystemAProxyServer(handler, &mockCloud{
				handleConn: func(srvObj systema.SystemAProxy_ReverseConnectionServer) error {
					closed := make(chan struct{})
					srvConnCh <- dnet.WrapAmbassadorCloudTunnelServer(srvObj, func() {
						close(closed)
					})
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

		client := systema.NewSystemAProxyClient(rawClient)
		cliObj, err := client.ReverseConnection(ctx)
		if err != nil {
			stop()
			return nil, nil, nil, err
		}
		cliConn := dnet.WrapAmbassadorCloudTunnelClient(cliObj)

		return cliConn, <-srvConnCh, stop, nil
	}
	t.Run("Client", func(t *testing.T) { nettest.TestConn(t, makePipe) })
	t.Run("Server", func(t *testing.T) { nettest.TestConn(t, flipMakePipe(makePipe)) })
}
