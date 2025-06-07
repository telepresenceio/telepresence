package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strconv"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/docker/go-plugins-helpers/network"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/teleroute"
)

type networkState struct {
	options

	ctx context.Context

	cancel context.CancelFunc

	pid int

	log logrus.FieldLogger

	// clientConn is connected to the Telepresence daemon telemount gRPC.
	clientConn *grpc.ClientConn
}

func newNetwork(ctx context.Context, logger logrus.FieldLogger, pid int, r *network.CreateNetworkRequest) (n *networkState, err error) {
	logger.Debugf("CreateNetwork, %v", r.Options)
	ctx, cancel := context.WithCancel(ctx)
	n = &networkState{
		ctx:    ctx,
		cancel: cancel,
		pid:    pid,
		log:    logger,
	}
	err = n.initialize(r)
	if err != nil {
		logrus.Error(err)
		return nil, err
	}
	return n, nil
}

func (n *networkState) initialize(r *network.CreateNetworkRequest) (err error) {
	var gws []netip.Prefix
	for _, ia := range r.IPv4Data {
		if ia.Gateway != "" {
			gw, err := netip.ParsePrefix(ia.Gateway)
			if err != nil {
				return fmt.Errorf("invalid IPv4 gateway address: %s", ia.Gateway)
			}
			gws = append(gws, gw)
		}
	}
	for _, ia := range r.IPv6Data {
		if ia.Gateway != "" {
			gw, err := netip.ParsePrefix(ia.Gateway)
			if err != nil {
				return fmt.Errorf("invalid IPv4 gateway address: %s", ia.Gateway)
			}
			gws = append(gws, gw)
		}
	}
	driverOpts, ok := r.Options["com.docker.network.generic"].(map[string]any)
	if !ok {
		return fmt.Errorf("network options are missing com.docker.network.generic")
	}
	err = n.options.parse(driverOpts)
	if err != nil {
		return err
	}
	time.Sleep(time.Second)
	return n.connectToDaemon(gws)
}

// callDaemon adds a short timeout when calling the daemon's gRPC so that we don't run into long waits in case something goes wrong.
func callDaemon[R proto.Message](ns *networkState, f func(ctx context.Context, client teleroute.TelerouteClient) (R, error)) (rsp R, err error) {
	ctx, cancel := context.WithTimeout(ns.ctx, 3*time.Second)
	rsp, err = f(ctx, teleroute.NewTelerouteClient(ns.clientConn))
	cancel()
	return
}

func (n *networkState) connectToDaemon(gateways []netip.Prefix) (err error) {
	ap := netip.AddrPortFrom(n.host, n.port)
	if n.clientConn != nil {
		n.clientConn.Close()
	}

	n.clientConn, err = grpc.NewClient(ap.String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		err = fmt.Errorf("unable to create gRPC connection to daemon: %w", err)
		n.log.Error(err)
		return err
	}
	defer func() {
		if err != nil {
			n.clientConn.Close()
		}
	}()

	gws := make([][]byte, len(gateways))
	for i, gw := range gateways {
		gws[i], err = gw.MarshalBinary()
		if err != nil {
			return err
		}
	}

	client := teleroute.NewTelerouteClient(n.clientConn)
	rq := &teleroute.ConnectRequest{Pid: int64(n.pid), Gateways: gws}

	var infoStream grpc.ServerStreamingClient[teleroute.Info]
	err = backoff.Retry(func() error {
		infoStream, err = client.Connect(n.ctx, rq)
		if status.Code(err) != codes.Unavailable {
			err = backoff.Permanent(err)
		}
		return err
	}, backoff.WithMaxRetries(backoff.NewConstantBackOff(50*time.Millisecond), 20))
	if err != nil {
		return err
	}

	go func() {
		defer n.cancel()
		for {
			info, err := infoStream.Recv()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					n.log.Errorf("error receiving info: %v", err)
				}
				break
			}
			im := info.GetInfo()
			n.log.Infof("Connected to %s version %s", im["name"], im["version"])
		}
	}()
	return nil
}

func (n *networkState) createEndpoint(r *network.CreateEndpointRequest) (_ *network.CreateEndpointResponse, err error) {
	endpointID := r.EndpointID
	eid := endpointID[:8]
	n.log.Debugf("Create endpoint %s %v %v", eid, r.Interface, r.Options)

	defer func() {
		if err != nil {
			n.log.Error(err)
		}
	}()

	var pfx netip.Prefix
	if len(r.Interface.Address) > 0 {
		pfx, err = netip.ParsePrefix(r.Interface.Address)
	} else if len(r.Interface.AddressIPv6) > 0 {
		pfx, err = netip.ParsePrefix(r.Interface.AddressIPv6)
	}
	if err != nil {
		return nil, err
	}

	daemon := false
	if daemonOpt, ok := r.Options["daemon"].(string); ok {
		daemon, _ = strconv.ParseBool(daemonOpt)
	}
	addr := pfx.Addr()
	binAddr, err := addr.MarshalBinary()
	if err != nil {
		return nil, err
	}
	_, err = callDaemon(n, func(ctx context.Context, client teleroute.TelerouteClient) (*emptypb.Empty, error) {
		return client.CreateEndpoint(ctx, &teleroute.CreateEndpointRequest{
			Id:      endpointID,
			Address: binAddr,
			Daemon:  daemon,
		})
	})
	return &network.CreateEndpointResponse{}, err
}

func (n *networkState) join(r *network.JoinRequest) (response *network.JoinResponse, err error) {
	endpointID := r.EndpointID
	n.log.Debugf("Join endpoint %.8s, sandbox %.8s %v", endpointID, r.SandboxKey, r.Options)

	rsp, err := callDaemon(n, func(ctx context.Context, client teleroute.TelerouteClient) (*teleroute.JoinResponse, error) {
		return client.Join(ctx, &teleroute.EndpointIdentifier{
			Id: endpointID,
		})
	})
	if err != nil {
		return nil, err
	}

	routeType := 1
	var viaStr string
	if len(rsp.Via) > 0 {
		var nxt netip.Addr
		err = nxt.UnmarshalBinary(rsp.Via)
		if err != nil {
			return nil, err
		}
		viaStr = nxt.String()
		routeType = 0
	}
	srs := make([]*network.StaticRoute, len(rsp.Routes))
	for i, r := range rsp.Routes {
		var pfx netip.Prefix
		err = pfx.UnmarshalBinary(r)
		if err != nil {
			return nil, err
		}
		srs[i] = &network.StaticRoute{
			Destination: pfx.String(),
			RouteType:   routeType,
			NextHop:     viaStr,
		}
	}
	response = &network.JoinResponse{
		InterfaceName: network.InterfaceName{
			SrcName:   rsp.InterfaceSrcName,
			DstPrefix: rsp.InterfaceDstPrefix,
		},
		StaticRoutes:          srs,
		DisableGatewayService: rsp.DisableGw,
	}
	if len(rsp.GwIpV4) > 0 {
		var gwIPv4 netip.Prefix
		err = gwIPv4.UnmarshalBinary(rsp.GwIpV4)
		if err != nil {
			return nil, err
		}
		response.Gateway = gwIPv4.Addr().String()
	}
	if len(rsp.GwIpV6) > 0 {
		var gwIPv6 netip.Prefix
		err = gwIPv6.UnmarshalBinary(rsp.GwIpV4)
		if err != nil {
			return nil, err
		}
		response.GatewayIPv6 = gwIPv6.Addr().String()
	}
	n.log.Debugf("Join response %v", response)
	return response, nil
}

func (n *networkState) leaveEndpoint(endpointID string) error {
	n.log.Debugf("Leave endpoint %.8s", endpointID)
	_, err := callDaemon(n, func(ctx context.Context, client teleroute.TelerouteClient) (*emptypb.Empty, error) {
		return client.Leave(ctx, &teleroute.EndpointIdentifier{
			Id: endpointID,
		})
	})
	if err != nil {
		code := status.Code(err)
		if code == codes.Canceled || code == codes.Unavailable {
			err = nil
		} else {
			n.log.Errorf("failed to leave endpoint %s: %v", endpointID, err)
		}
	}
	return err
}

func (n *networkState) deleteEndpoint(endpointID string) error {
	n.log.Debugf("Delete endpoint %.8s", endpointID)
	_, err := callDaemon(n, func(ctx context.Context, client teleroute.TelerouteClient) (*emptypb.Empty, error) {
		return client.RemoveEndpoint(ctx, &teleroute.EndpointIdentifier{Id: endpointID})
	})
	if err != nil {
		code := status.Code(err)
		if code == codes.Canceled || code == codes.Unavailable {
			err = nil
		} else {
			n.log.Errorf("failed to leave endpoint %s: %v", endpointID, err)
		}
	}
	return err
}
