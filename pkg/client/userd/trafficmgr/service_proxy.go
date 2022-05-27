package trafficmgr

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/dlog"
	managerrpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// mgrProxy implements rpc.ManagerServer, but just proxies all requests through a rpc.ManagerClient.
type mgrProxy struct {
	sync.RWMutex
	clientX      managerrpc.ManagerClient
	callOptionsX []grpc.CallOption

	managerrpc.UnsafeManagerServer
}

type ManagerProxy interface {
	managerrpc.ManagerServer

	// SetClient replaces the client of this proxy
	SetClient(client managerrpc.ManagerClient, callOptions ...grpc.CallOption)
}

// NewManagerProxy returns a rpc.ManagerServer that just proxies all requests through the given rpc.ManagerClient.
func NewManagerProxy() ManagerProxy {
	return &mgrProxy{}
}

func (p *mgrProxy) SetClient(client managerrpc.ManagerClient, callOptions ...grpc.CallOption) {
	p.Lock()
	p.clientX = client
	p.callOptionsX = callOptions
	p.Unlock()
}

func (p *mgrProxy) get() (managerrpc.ManagerClient, []grpc.CallOption, error) {
	p.RLock()
	defer p.RUnlock()
	if p.clientX == nil {
		return nil, nil, status.Error(codes.Unavailable, "telepresence: the userd is not connected to the manager")
	}
	return p.clientX, p.callOptionsX, nil
}

func (p *mgrProxy) GetIntercept(ctx context.Context, arg *managerrpc.GetInterceptRequest) (*managerrpc.InterceptInfo, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.GetIntercept(ctx, arg, callOptions...)
}

func (p *mgrProxy) Version(ctx context.Context, arg *empty.Empty) (*managerrpc.VersionInfo2, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.Version(ctx, arg, callOptions...)
}
func (p *mgrProxy) GetLicense(ctx context.Context, arg *empty.Empty) (*managerrpc.License, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.GetLicense(ctx, arg, callOptions...)
}

func (p *mgrProxy) GetTelepresenceAPI(ctx context.Context, arg *empty.Empty) (*managerrpc.TelepresenceAPIInfo, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.GetTelepresenceAPI(ctx, arg, callOptions...)
}

func (p *mgrProxy) CanConnectAmbassadorCloud(ctx context.Context, arg *empty.Empty) (*managerrpc.AmbassadorCloudConnection, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.CanConnectAmbassadorCloud(ctx, arg, callOptions...)
}

func (p *mgrProxy) GetCloudConfig(ctx context.Context, arg *empty.Empty) (*managerrpc.AmbassadorCloudConfig, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	// TODO (dyung): We might want to make this always return an error since the client should already have the config.
	return client.GetCloudConfig(ctx, arg, callOptions...)
}

func (p *mgrProxy) ArriveAsClient(ctx context.Context, arg *managerrpc.ClientInfo) (*managerrpc.SessionInfo, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.ArriveAsClient(ctx, arg, callOptions...)
}
func (p *mgrProxy) ArriveAsAgent(ctx context.Context, arg *managerrpc.AgentInfo) (*managerrpc.SessionInfo, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.ArriveAsAgent(ctx, arg, callOptions...)
}
func (p *mgrProxy) Remain(ctx context.Context, arg *managerrpc.RemainRequest) (*empty.Empty, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.Remain(ctx, arg, callOptions...)
}
func (p *mgrProxy) Depart(ctx context.Context, arg *managerrpc.SessionInfo) (*empty.Empty, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.Depart(ctx, arg, callOptions...)
}

func (p *mgrProxy) WatchAgents(arg *managerrpc.SessionInfo, srv managerrpc.Manager_WatchAgentsServer) error {
	return status.Error(codes.Unimplemented, "WatchAgents was deprecated in 2.5.5")
}

func (p *mgrProxy) WatchAgentsNS(arg *managerrpc.AgentsRequest, srv managerrpc.Manager_WatchAgentsNSServer) error {
	client, callOptions, err := p.get()
	if err != nil {
		return err
	}
	cli, err := client.WatchAgentsNS(srv.Context(), arg, callOptions...)
	if err != nil {
		return err
	}
	for {
		snapshot, err := cli.Recv()
		if err != nil {
			if err == io.EOF || srv.Context().Err() != nil {
				return nil
			}
			return err
		}
		if err = srv.Send(snapshot); err != nil {
			return err
		}
	}
}

func (p *mgrProxy) WatchIntercepts(arg *managerrpc.SessionInfo, srv managerrpc.Manager_WatchInterceptsServer) error {
	client, callOptions, err := p.get()
	if err != nil {
		return err
	}
	cli, err := client.WatchIntercepts(srv.Context(), arg, callOptions...)
	if err != nil {
		return err
	}
	for {
		snapshot, err := cli.Recv()
		if err != nil {
			if err == io.EOF || srv.Context().Err() != nil {
				return nil
			}
			return err
		}
		if err = srv.Send(snapshot); err != nil {
			return err
		}
	}
}

func (p *mgrProxy) PrepareIntercept(_ context.Context, _ *managerrpc.CreateInterceptRequest) (*managerrpc.PreparedIntercept, error) {
	return nil, errors.New("must call connector.CanIntercept instead of manager.CreateIntercept")
}
func (p *mgrProxy) CreateIntercept(_ context.Context, _ *managerrpc.CreateInterceptRequest) (*managerrpc.InterceptInfo, error) {
	return nil, errors.New("must call connector.CreateIntercept instead of manager.CreateIntercept")
}
func (p *mgrProxy) RemoveIntercept(context.Context, *managerrpc.RemoveInterceptRequest2) (*empty.Empty, error) {
	return nil, status.Error(codes.Unimplemented, "must call connector.RemoveIntercept instead of manager.RemoveIntercept")
}
func (p *mgrProxy) UpdateIntercept(ctx context.Context, arg *managerrpc.UpdateInterceptRequest) (*managerrpc.InterceptInfo, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.UpdateIntercept(ctx, arg, callOptions...)
}
func (p *mgrProxy) ReviewIntercept(ctx context.Context, arg *managerrpc.ReviewInterceptRequest) (*empty.Empty, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.ReviewIntercept(ctx, arg, callOptions...)
}

func (p *mgrProxy) ClientTunnel(managerrpc.Manager_ClientTunnelServer) error {
	return status.Error(codes.Unimplemented, "ClientTunnel was deprecated in 2.4.5 and has since been removed")
}

func (p *mgrProxy) AgentTunnel(managerrpc.Manager_AgentTunnelServer) error {
	return status.Error(codes.Unimplemented, "AgentTunnel was deprecated in 2.4.5 and has since been removed")
}

type tmReceiver interface {
	Recv() (*managerrpc.TunnelMessage, error)
}

type tmSender interface {
	Send(*managerrpc.TunnelMessage) error
}

func recvLoop(ctx context.Context, who string, in tmReceiver, out chan<- *managerrpc.TunnelMessage, wg *sync.WaitGroup) {
	defer func() {
		dlog.Debugf(ctx, "%s Recv loop ended", who)
		wg.Done()
	}()
	dlog.Debugf(ctx, "%s Recv loop started", who)
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

func sendLoop(ctx context.Context, who string, out tmSender, in <-chan *managerrpc.TunnelMessage, wg *sync.WaitGroup) {
	defer func() {
		dlog.Debugf(ctx, "%s Send loop ended", who)
		wg.Done()
	}()
	dlog.Debugf(ctx, "%s Send loop started", who)
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
		case payload := <-in:
			if payload == nil {
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

func (p *mgrProxy) Tunnel(fhClient managerrpc.Manager_TunnelServer) error {
	client, callOptions, err := p.get()
	if err != nil {
		return err
	}
	ctx := fhClient.Context()
	fhManager, err := client.Tunnel(ctx, callOptions...)
	if err != nil {
		return err
	}
	mgrToClient := make(chan *managerrpc.TunnelMessage)
	clientToMgr := make(chan *managerrpc.TunnelMessage)

	wg := sync.WaitGroup{}
	wg.Add(4)
	go recvLoop(ctx, "manager", fhManager, mgrToClient, &wg)
	go sendLoop(ctx, "manager", fhManager, clientToMgr, &wg)
	go recvLoop(ctx, "client", fhClient, clientToMgr, &wg)
	go sendLoop(ctx, "client", fhClient, mgrToClient, &wg)
	wg.Wait()
	return nil
}

func (p *mgrProxy) WatchDial(arg *managerrpc.SessionInfo, srv managerrpc.Manager_WatchDialServer) error {
	client, callOptions, err := p.get()
	if err != nil {
		return err
	}
	cli, err := client.WatchDial(srv.Context(), arg, callOptions...)
	if err != nil {
		return err
	}
	for {
		request, err := cli.Recv()
		if err != nil {
			if err == io.EOF || srv.Context().Err() != nil {
				return nil
			}
			return err
		}
		if err = srv.Send(request); err != nil {
			return err
		}
	}
}

func (p *mgrProxy) LookupHost(ctx context.Context, arg *managerrpc.LookupHostRequest) (*managerrpc.LookupHostResponse, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.LookupHost(ctx, arg, callOptions...)
}

func (p *mgrProxy) AgentLookupHostResponse(ctx context.Context, arg *managerrpc.LookupHostAgentResponse) (*empty.Empty, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.AgentLookupHostResponse(ctx, arg, callOptions...)
}

func (p *mgrProxy) WatchLookupHost(*managerrpc.SessionInfo, managerrpc.Manager_WatchLookupHostServer) error {
	return status.Error(codes.Unimplemented, "must call manager.WatchLookupHost from an agent (intercepted Pod), not from a client (workstation)")
}

func (p *mgrProxy) WatchClusterInfo(arg *managerrpc.SessionInfo, srv managerrpc.Manager_WatchClusterInfoServer) error {
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

func (p *mgrProxy) SetLogLevel(ctx context.Context, request *managerrpc.LogLevelRequest) (*empty.Empty, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.SetLogLevel(ctx, request, callOptions...)
}

func (p *mgrProxy) GetLogs(context.Context, *managerrpc.GetLogsRequest) (*managerrpc.LogsResponse, error) {
	return nil, status.Error(codes.Unimplemented, " \"must call connector.GatherLogs instead of manager.GetLogs\"")
}

func (p *mgrProxy) WatchLogLevel(*empty.Empty, managerrpc.Manager_WatchLogLevelServer) error {
	return status.Error(codes.Unimplemented, "must call manager.WatchLogLevel from an agent (intercepted Pod), not from a client (workstation)")
}
