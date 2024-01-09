package daemon

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// mgrProxy implements connector.ManagerProxyServer, but just proxies all requests through a manager.ManagerClient.
type mgrProxy struct {
	sync.RWMutex
	clientX      manager.ManagerClient
	callOptionsX []grpc.CallOption

	connector.UnsafeManagerProxyServer
}

var _ connector.ManagerProxyServer = &mgrProxy{}

func (p *mgrProxy) setClient(client manager.ManagerClient, callOptions ...grpc.CallOption) {
	p.Lock()
	p.clientX = client
	p.callOptionsX = callOptions
	p.Unlock()
}

func (p *mgrProxy) get() (manager.ManagerClient, []grpc.CallOption, error) {
	p.RLock()
	defer p.RUnlock()
	if p.clientX == nil {
		return nil, nil, status.Error(codes.Unavailable, "telepresence: the userd is not connected to the manager")
	}
	return p.clientX, p.callOptionsX, nil
}

func (p *mgrProxy) Version(ctx context.Context, arg *emptypb.Empty) (*manager.VersionInfo2, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.Version(ctx, arg, callOptions...)
}

func (p *mgrProxy) GetClientConfig(ctx context.Context, arg *emptypb.Empty) (*manager.CLIConfig, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.GetClientConfig(ctx, arg, callOptions...)
}

type tmReceiver interface {
	Recv() (*manager.TunnelMessage, error)
}

type tmSender interface {
	Send(*manager.TunnelMessage) error
}

func recvLoop(ctx context.Context, who string, in tmReceiver, out chan<- *manager.TunnelMessage, wg *sync.WaitGroup) {
	defer func() {
		dlog.Tracef(ctx, "%s Recv loop ended", who)
		close(out)
		wg.Done()
	}()
	dlog.Tracef(ctx, "%s Recv loop started", who)
	for {
		payload, err := in.Recv()
		if err != nil {
			if ctx.Err() == nil && !(errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed)) {
				dlog.Errorf(ctx, "Tunnel %s.Recv() failed: %v", who, err)
			}
			return
		}
		dlog.Tracef(ctx, "<- %s %d", who, len(payload.Payload))
		select {
		case <-ctx.Done():
			return
		case out <- payload:
		}
	}
}

func sendLoop(ctx context.Context, who string, out tmSender, in <-chan *manager.TunnelMessage, wg *sync.WaitGroup) {
	defer func() {
		dlog.Tracef(ctx, "%s Send loop ended", who)
		wg.Done()
	}()
	dlog.Tracef(ctx, "%s Send loop started", who)
	if outC, ok := out.(interface{ CloseSend() error }); ok {
		defer func() {
			if err := outC.CloseSend(); err != nil {
				dlog.Errorf(ctx, "CloseSend() failed: %v", err)
			}
		}()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case payload, ok := <-in:
			if !ok {
				return
			}
			if err := out.Send(payload); err != nil {
				if !errors.Is(err, net.ErrClosed) {
					dlog.Errorf(ctx, "Tunnel %s.Send() failed: %v", who, err)
				}
				return
			}
			dlog.Tracef(ctx, "-> %s %d", who, len(payload.Payload))
		}
	}
}

func (p *mgrProxy) Tunnel(fhClient connector.ManagerProxy_TunnelServer) error {
	client, callOptions, err := p.get()
	if err != nil {
		return err
	}
	ctx := fhClient.Context()
	fhManager, err := client.Tunnel(ctx, callOptions...)
	if err != nil {
		return err
	}
	mgrToClient := make(chan *manager.TunnelMessage)
	clientToMgr := make(chan *manager.TunnelMessage)

	wg := sync.WaitGroup{}
	wg.Add(4)
	go recvLoop(ctx, "manager", fhManager, mgrToClient, &wg)
	go sendLoop(ctx, "manager", fhManager, clientToMgr, &wg)
	go recvLoop(ctx, "client", fhClient, clientToMgr, &wg)
	go sendLoop(ctx, "client", fhClient, mgrToClient, &wg)
	wg.Wait()
	return nil
}

func (p *mgrProxy) EnsureAgent(ctx context.Context, arg *manager.EnsureAgentRequest) (*emptypb.Empty, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.EnsureAgent(ctx, arg, callOptions...)
}

func (p *mgrProxy) LookupDNS(ctx context.Context, arg *manager.DNSRequest) (*manager.DNSResponse, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.LookupDNS(ctx, arg, callOptions...)
}

func (p *mgrProxy) WatchClusterInfo(arg *manager.SessionInfo, srv connector.ManagerProxy_WatchClusterInfoServer) error {
	client, callOptions, err := p.get()
	if err != nil {
		return err
	}
	cli, err := client.WatchClusterInfo(srv.Context(), arg, callOptions...)
	if err != nil {
		return err
	}
	for {
		info, err := cli.Recv()
		if err != nil {
			if err == io.EOF || srv.Context().Err() != nil {
				return nil
			}
			return err
		}
		if err = srv.Send(info); err != nil {
			return err
		}
	}
}
