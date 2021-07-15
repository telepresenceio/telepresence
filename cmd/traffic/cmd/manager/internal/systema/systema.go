// Package system implements all of the yucky system-logic details for communicating with System A
// from the Telepresence manager.
//
// The business-logic code (or at least the code one layer closer to being business-logic than this
// package is) must implement the ManagerServer interface, and call the ConnectToSystemA() function
// as appropriate.
package systema

import (
	"context"
	"fmt"
	"io"
	"net"
	"time"

	"google.golang.org/grpc"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/rpc/v2/systema"
	"github.com/telepresenceio/telepresence/v2/pkg/dnet"
)

// ManagerServer is the interface that you must implement for when System A talks to the Manager.
type ManagerServer interface {
	manager.ManagerServer
	DialIntercept(ctx context.Context, interceptID string) (net.Conn, error)
}

type server struct {
	ManagerServer
	manager.UnsafeManagerProxyServer
}

// HandleConnection implements manager.ManagerProxyServer
func (s server) HandleConnection(rawconn manager.ManagerProxy_HandleConnectionServer) error {
	ctx := rawconn.Context()

	interceptID, systemaConn, err := dnet.AcceptFromAmbassadorCloud(rawconn)
	if err != nil {
		return fmt.Errorf("HandleConnection: accept: %w", err)
	}

	dlog.Infof(ctx, "HandleConnection: handling connection for intercept %q", interceptID)

	interceptConn, err := s.DialIntercept(ctx, interceptID)
	if err != nil {
		err = fmt.Errorf("HandleConnection: accept: %w", err)
		dlog.Errorln(ctx, err)
		return err
	}

	grp := dgroup.NewGroup(ctx, dgroup.GroupConfig{})

	grp.Go("pump-recv", func(_ context.Context) error {
		if _, err := io.Copy(interceptConn, systemaConn); err != nil {
			err = fmt.Errorf("HandleConnection: pump cluster<-systema: %w", err)
			dlog.Errorln(ctx, err)
			return err
		}
		return nil
	})
	grp.Go("pump-send", func(_ context.Context) error {
		if _, err := io.Copy(systemaConn, interceptConn); err != nil {
			err = fmt.Errorf("HandleConnection: pump systema<-cluster: %w", err)
			dlog.Errorln(ctx, err)
			return err
		}
		return nil
	})

	return grp.Wait()
}

// ConnectToSystemA initiates a connection to System A.
//
// The selfService argument is the ManagerServer implmentation that System A may make RPC calls to.
//
// Shut down the connection by cancelling the context, then call the returned wait() function to
// wait for the connection to fully shut down.
func ConnectToSystemA(ctx context.Context,
	self ManagerServer,
	systemaAddr string, systemaDialOpts ...grpc.DialOption,
) (client systema.SystemACRUDClient, wait func() error, err error) {
	conn, err := grpc.DialContext(ctx, systemaAddr, systemaDialOpts...)
	if err != nil {
		return nil, nil, err
	}
	systemaCRUD := systema.NewSystemACRUDClient(conn)

	listener, addConn := dnet.NewLoopbackListener()

	grp := dgroup.NewGroup(ctx, dgroup.GroupConfig{})

	grp.Go("server", func(ctx context.Context) error {
		grpcServer := grpc.NewServer()
		serverImpl := server{ManagerServer: self}
		manager.RegisterManagerServer(grpcServer, serverImpl)
		manager.RegisterManagerProxyServer(grpcServer, serverImpl)

		sc := &dhttp.ServerConfig{
			Handler: grpcServer,
		}
		return sc.Serve(ctx, listener)
	})
	grp.Go("client", func(ctx context.Context) error {
		defer conn.Close()
		systemaProxy := systema.NewSystemAProxyClient(conn)
		var tempDelay time.Duration
		for ctx.Err() == nil {
			err := func() error {
				rconnInner, err := systemaProxy.ReverseConnection(ctx)
				if err != nil {
					return err
				}
				dlog.Info(ctx, "connection to System A established")
				rconn := dnet.WrapAmbassadorCloudTunnel(rconnInner)
				if err := addConn(rconn); err != nil {
					return err
				}
				// Wait until the above "server" goroutine is done with the
				// connection.
				return rconn.Wait()
			}()
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				// This is the almost the same backoff that net/http.Server uses.
				// I've bumped the max backoff from 1s to 5s, but it may still need
				// to be tuned for our use-case.
				if tempDelay == 0 {
					tempDelay = 5 * time.Millisecond
				} else {
					tempDelay *= 2
				}
				if max := 5 * time.Second; tempDelay > max {
					tempDelay = max
				}

				dlog.Warnf(ctx, "error talking to System A: %v; retrying in %v", err, tempDelay)
				select {
				case <-time.After(tempDelay):
				case <-ctx.Done():
				}
			} else {
				dlog.Info(ctx, "connection to System A terminated gracefully")
				tempDelay = 0
			}
		}
		return nil
	})

	return systemaCRUD, grp.Wait, nil
}
