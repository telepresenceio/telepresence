// Package telepresence implements all of the yucky systems-logic details for communicating with the
// Telepresence manager from System A.
package telepresence

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"

	"github.com/golang/protobuf/ptypes/empty"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dutil"
	"github.com/datawire/telepresence2/pkg/rpc/manager2systema"
	"github.com/datawire/telepresence2/pkg/rpc/systema2manager"
	"github.com/datawire/telepresence2/pkg/systemaconn"
)

type ManagerClient interface {
	systema2manager.ManagerCRUDClient
	systema2manager.ManagerProxyClient
}

type managerClient struct {
	systema2manager.ManagerCRUDClient
	systema2manager.ManagerProxyClient
}

type ManagerPool interface {
	PutManager(managerID string, obj ManagerClient)
	DelManager(managerID string)
	GetManager(managerID string) ManagerClient

	// RouteSystemARequest takes a request made from the "systema" service that needs to be
	// routed to a Telepresence manager, and returns the ID of the manager that it needs to be
	// routed to.  (Note: A metadata.MD just the gRPC library's version of an http.Header.)  If
	// a routing decision cannot be made, return an empty string.
	RouteSystemARequest(metadata.MD) (managerID string)

	// ManagerRequestAuthentication takes a request from a Telepresence manager, and returns the
	// manager ID based on the authentication credentials in the request.  (Note: A metadata.MD
	// just the gRPC library's version of an http.Header.)
	ManagerRequestAuthentication(metadata.MD) (managerID string)

	// RoutePreviewRequest takes an end-user request to a preview URL that needs to be proxied
	// in to a user's cluster, and returns the ID of the manager to proxy to, and also the
	// intercept ID.
	RoutePreviewRequest(http.Header) (managerID, interceptID string)
}

// Server handles all the yucky systems-logic details for communicating with the Telepresence
// manager from System A.  Every exported field of this struct must be filled in.
type Server struct {
	// GRPCAddr is where to listen on for gRPC requests.
	GRPCListener net.Listener
	// ProxyAddr is where to listen on for end-user preview requests that get proxied in to the
	// user's cluster.
	ProxyListener net.Listener

	// CRUDServer is the implementation for when a Telepresence manager makes calls to this
	// System A instance.
	CRUDServer manager2systema.SystemACRUDServer

	// ManagerPool is the implementation for managing the pool of connections from/to
	// Telepresence managers.
	ManagerPool ManagerPool

	manager2systema.UnimplementedSystemAProxyServer
	systema2manager.UnimplementedManagerCRUDServer
}

type serverHandler struct {
	*Server
}

// ListIntercepts recieves a ListIntercepts request, uses your RouteSystemARequest function to
// inspect the HTTP headers to decide which Telepresence manager to route that to, then proxies it
// to that manager.
func (srv serverHandler) ListIntercepts(ctx context.Context, req *empty.Empty) (*systema2manager.InterceptInfoSnapshot, error) {
	md, haveMD := metadata.FromIncomingContext(ctx)
	if !haveMD {
		// The gRPC server 100% should have handed us a Context that has metadata attached
		// to it.
		panic("this should not happen")
	}
	managerID := srv.ManagerPool.RouteSystemARequest(md)
	manager := srv.ManagerPool.GetManager(managerID)
	if manager == nil {
		return nil, fmt.Errorf("manager ID %q is not connected", managerID)
	}
	return manager.ListIntercepts(ctx, req)
}

// RemoveIntercept recieves a RemoveIntercept request, uses your RouteSystemARequest function to
// inspect the HTTP headers to decide which Telepresence manager to route that to, then proxies it
// to that manager.
func (srv serverHandler) RemoveIntercept(ctx context.Context, req *systema2manager.RemoveInterceptRequest) (*empty.Empty, error) {
	md, haveMD := metadata.FromIncomingContext(ctx)
	if !haveMD {
		// The gRPC server 100% should have handed us a Context that has metadata attached
		// to it.
		panic("this should not happen")
	}
	managerID := srv.ManagerPool.RouteSystemARequest(md)
	manager := srv.ManagerPool.GetManager(managerID)
	if manager == nil {
		return nil, fmt.Errorf("manager ID %q is not connected", managerID)
	}
	return manager.RemoveIntercept(ctx, req)
}

// ReverseConnection handles a ReverseConnection reequest from a Telepresence manager.
func (srv serverHandler) ReverseConnection(rawConn manager2systema.SystemAProxy_ReverseConnectionServer) error {
	ctx := rawConn.Context()
	md, haveMD := metadata.FromIncomingContext(ctx)
	if !haveMD {
		// The gRPC server 100% should have handed us a Context that has metadata attached
		// to it.
		panic("this should not happen")
	}
	managerID := srv.ManagerPool.ManagerRequestAuthentication(md)

	netConn := systemaconn.Wrap(rawConn)

	grpcConn, err := grpc.DialContext(rawConn.Context(), managerID,
		grpc.WithDialer(func(string, time.Duration) (net.Conn, error) {
			return netConn, nil
		}),
		grpc.WithInsecure())
	if err != nil {
		return fmt.Errorf("SystemA->Telepresence: dial: %w", err)
	}

	srv.ManagerPool.PutManager(managerID, managerClient{
		ManagerCRUDClient:  systema2manager.NewManagerCRUDClient(grpcConn),
		ManagerProxyClient: systema2manager.NewManagerProxyClient(grpcConn),
	})
	err = netConn.Wait()
	srv.ManagerPool.DelManager(managerID)
	if err != nil {
		return fmt.Errorf("SystemA->Telepresence: %w", err)
	}
	return nil
}

// Serve runs server until a fatal error occurs or the Context is cancelled.
func (srv *Server) Serve(ctx context.Context) error {
	grp := dgroup.NewGroup(ctx, dgroup.GroupConfig{})

	grp.Go("grpc-server", func(ctx context.Context) error {
		// This is both where where the Telepresence managers dial to (or rather, this is
		// where Envoy routes requests from Telepresence managers to), and also where the
		// "systema" service dials to when it wants to make an RPC to the Telepresence
		// manager.
		grpcHandler := grpc.NewServer()
		server := &http.Server{
			ErrorLog: dlog.StdLogger(dlog.WithField(ctx, "SUB", "http-server"), dlog.LogLevelError),
			Handler: h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx := r.Context()
				ctx = dlog.WithField(ctx, "SUB", "http-server/handler")
				dlog.Infof(ctx, "gRPC request: %q", r.URL)
				r = r.WithContext(ctx)

				if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
					grpcHandler.ServeHTTP(w, r)
				} else {
					http.Error(w,
						"this endpoint only supports gRPC, not plain HTTP requests",
						http.StatusBadRequest)
				}
			}), &http2.Server{}),
		}

		systema2manager.RegisterManagerCRUDServer(grpcHandler, serverHandler{srv})
		manager2systema.RegisterSystemACRUDServer(grpcHandler, srv.CRUDServer)
		manager2systema.RegisterSystemAProxyServer(grpcHandler, serverHandler{srv})

		return dutil.ServeHTTPWithContext(ctx, server, srv.GRPCListener)
	})
	grp.Go("proxy-server", func(ctx context.Context) error {
		// This is where the "systema" service configures Envoy to dial to when it wants to
		// send preview-URL traffic to a cluster.
		server := &http.Server{
			ErrorLog: dlog.StdLogger(dlog.WithField(ctx, "SUB", "http-server"), dlog.LogLevelError),
			Handler: h2c.NewHandler(&httputil.ReverseProxy{
				Director: func(r *http.Request) {
					dlog.Infof(r.Context(), "proxy request: %q", r.URL)
					managerID, interceptID := srv.ManagerPool.RoutePreviewRequest(r.Header)
					r.URL.Scheme = "http"
					r.URL.Host = base64.RawURLEncoding.EncodeToString([]byte(managerID + ":" + interceptID))
				},
				Transport: &http.Transport{
					DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
						host, _, err := net.SplitHostPort(addr)
						if err != nil {
							// The httputil.ReverseProxy should always
							// pass us a valid host:port pair.
							panic("should not happen")
						}
						mipair, err := base64.RawURLEncoding.DecodeString(host)
						if err != nil {
							// This had to have been set by r.URL.Host
							// in the Director above; we *know* that it
							// is valid base64.
							panic("should not happen")
						}
						parts := strings.SplitN(string(mipair), ":", 2)
						if len(parts) != 2 {
							// This had to have been set by r.URL.Host
							// in the Director above; we *know* that it
							// contains a colon.
							panic("should not happen")
						}
						managerID, interceptID := parts[0], parts[1]
						manager := srv.ManagerPool.GetManager(managerID)
						if manager == nil {
							return nil, fmt.Errorf("HandleConnection: dial: manager ID %q is not connected", managerID)
						}
						conn, err := systemaconn.DialToManager(ctx, manager, interceptID)
						if err != nil {
							return nil, fmt.Errorf("HandleConnection: dial: %w", err)
						}
						return conn, nil
					},
					// These are from http.DefaultTransport
					ForceAttemptHTTP2:     true,
					MaxIdleConns:          100,
					IdleConnTimeout:       90 * time.Second,
					TLSHandshakeTimeout:   10 * time.Second,
					ExpectContinueTimeout: 1 * time.Second,
				},
			}, &http2.Server{}),
		}
		return dutil.ServeHTTPWithContext(ctx, server, srv.ProxyListener)
	})

	return grp.Wait()
}
