package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"

	"google.golang.org/grpc/metadata"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/telepresence2/cmd/tst-systema/telepresence"
)

type ManagerPool struct {
	mu   sync.Mutex
	pool map[string]telepresence.ManagerClient
}

func (p *ManagerPool) PutManager(managerID string, obj telepresence.ManagerClient) {
	p.mu.Lock()
	defer p.mu.Unlock()
	dlog.Infoln(context.TODO(), "PutManager:", managerID)
	p.pool[managerID] = obj
}
func (p *ManagerPool) DelManager(managerID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	dlog.Infoln(context.TODO(), "DelManager:", managerID)
	delete(p.pool, managerID)
}
func (p *ManagerPool) GetManager(managerID string) telepresence.ManagerClient {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pool[managerID]
}

func (*ManagerPool) RouteSystemARequest(header metadata.MD) (managerID string) {
	vals := header.Get("X-Telepresence-ManagerID")
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}
func (*ManagerPool) ManagerRequestAuthentication(header metadata.MD) (managerID string) {
	vals := header.Get("X-Telepresence-ManagerID")
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}
func (p *ManagerPool) RoutePreviewRequest(header http.Header) (managerID, interceptID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for managerID := range p.pool {
		return managerID, "todo"
	}
	return "", ""
}

func main() {
	grp := dgroup.NewGroup(context.Background(), dgroup.GroupConfig{
		EnableSignalHandling: true,
	})

	grp.Go("main", func(ctx context.Context) error {
		grpcListener, err := net.Listen("tcp", ":8000")
		if err != nil {
			return err
		}
		dlog.Infof(ctx, "Listening for gRPC requests on %v", grpcListener.Addr())

		proxyListener, err := net.Listen("tcp", ":8001")
		if err != nil {
			return err
		}
		dlog.Infof(ctx, "Listening for preview-URL proxy requests on %v", proxyListener.Addr())

		server := &telepresence.Server{
			GRPCListener:  grpcListener,
			ProxyListener: proxyListener,
			CRUDServer:    nil, // TODO
			ManagerPool: &ManagerPool{
				pool: make(map[string]telepresence.ManagerClient),
			},
		}

		return server.Serve(ctx)
	})

	if err := grp.Wait(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
	}
}
