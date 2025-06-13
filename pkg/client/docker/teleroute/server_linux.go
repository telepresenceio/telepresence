// Package teleroute contains the Telepresence Daemon Teleroute service that the Docker Network Plugin with the
// same name connects to.
package teleroute

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"net"
	"net/netip"
	"sync"

	"github.com/puzpuzpuz/xsync/v3"
	"github.com/vishvananda/netlink"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/teleroute"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	grpcServer "github.com/telepresenceio/telepresence/v2/pkg/grpc/server"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
	"github.com/telepresenceio/telepresence/v2/pkg/vif"
)

const bridgeName = "br-daemon"

type linkNotFoundError struct {
	name string
	err  error
}

func (e linkNotFoundError) Error() string {
	return fmt.Sprintf("link %q not found: %s", e.name, e.err.Error())
}

func (e linkNotFoundError) Unwrap() error {
	return e.err
}

type endpoint struct {
	addr     netip.Addr
	macAddr  net.HardwareAddr
	vethCont netlink.Link
	vethHost netlink.Link
	daemon   bool
}

type server struct {
	rpc.UnsafeTelerouteServer
	done          <-chan struct{}
	watchersMutex sync.Mutex
	pluginPid     int
	routesCh      <-chan []netip.Prefix
	currentRoutes []netip.Prefix
	gateways      []netip.Prefix
	tap           *vif.TunnelingDevice
	bridgeIdx     int
	endpoints     *xsync.MapOf[string, endpoint]
	endpointCache *xsync.MapOf[netip.Addr, [2]netlink.Link]
	port          uint16
	daemonAddr    netip.Addr
}

func StartServer(g *dgroup.Group, tap *vif.TunnelingDevice, routesCh <-chan []netip.Prefix, teleroutePort uint16) (Server, error) {
	ts := &server{
		routesCh:      routesCh,
		done:          make(chan struct{}),
		tap:           tap,
		port:          teleroutePort,
		endpoints:     xsync.NewMapOf[string, endpoint](),
		endpointCache: xsync.NewMapOf[netip.Addr, [2]netlink.Link](),
	}
	g.Go("teleroute", ts.serve)
	return ts, nil
}

func (ts *server) DaemonAddress() netip.Addr {
	return ts.daemonAddr
}

func (ts *server) Connect(cr *rpc.ConnectRequest, connectServer grpc.ServerStreamingServer[rpc.Info]) error {
	gws := make([]netip.Prefix, len(cr.Gateways))
	for i, gw := range cr.Gateways {
		err := gws[i].UnmarshalBinary(gw)
		if err != nil {
			return status.Error(codes.InvalidArgument, err.Error())
		}
	}
	ts.pluginPid = int(cr.Pid)
	ts.gateways = gws
	err := connectServer.Send(&rpc.Info{Info: map[string]string{"name": client.ProcessName(), "version": version.Structured.String()}})
	if err != nil {
		return status.Error(codes.Aborted, "failed to send initial response")
	}

	// The termination of this stream tells the network plugin that the daemon is shutting down.
	select {
	case <-ts.done:
	case <-connectServer.Context().Done():
	}
	return nil
}

func (ts *server) CreateEndpoint(ctx context.Context, request *rpc.CreateEndpointRequest) (*emptypb.Empty, error) {
	err := ts.createEndpoint(ctx, request)
	if status.Code(err) == codes.Unknown {
		err = status.Error(codes.Internal, err.Error())
	}
	return &emptypb.Empty{}, err
}

func (ts *server) Join(ctx context.Context, request *rpc.EndpointIdentifier) (*rpc.JoinResponse, error) {
	rsp, err := ts.join(ctx, request)
	if status.Code(err) == codes.Unknown {
		err = status.Error(codes.Internal, err.Error())
	}
	return rsp, err
}

// createAddressEndpoint creates a veth pair with one foot in the daemon's namespace and the other in the network plugin's namespace.
// The latter is what Docker then moves in to a joining container's namespace and then returns when the container is leaving.
// The fact that Docker returns this interface makes it possible for us to reuse the veth pair, so we cache those pairs here
// by the IP of the joining container (Docker frequently reuses those IPs).
func (ts *server) createAddressEndpoint(ctx context.Context) ([2]netlink.Link, error) {
	pair, err := createVethPair(ctx)
	if err != nil {
		return pair, err
	}
	br, err := ts.bridge()
	if err != nil {
		return pair, err
	}
	vhn := pair[0].Attrs().Name
	brn := br.Attrs().Name
	dlog.Debugf(ctx, "link set %s master %s", vhn, brn)
	err = netlink.LinkSetMaster(pair[0], br)
	if err != nil {
		return pair, fmt.Errorf("link set %s master %s failed: %w", vhn, brn, err)
	}
	vcn := pair[1].Attrs().Name
	dlog.Debugf(ctx, "link set %s netns %d", vcn, ts.pluginPid)
	err = netlink.LinkSetNsPid(pair[1], ts.pluginPid)
	if err != nil {
		return pair, fmt.Errorf("link set %s netns %d failed: %w", vcn, ts.pluginPid, err)
	}
	return pair, err
}

func (ts *server) createEndpoint(ctx context.Context, request *rpc.CreateEndpointRequest) error {
	var addr netip.Addr
	err := addr.UnmarshalBinary(request.Address)
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	if request.Daemon {
		ts.daemonAddr = addr
	}
	_, loaded := ts.endpoints.LoadOrCompute(request.Id, func() (ep endpoint) {
		pair, _ := ts.endpointCache.LoadOrCompute(addr, func() (pair [2]netlink.Link) {
			pair, err = ts.createAddressEndpoint(ctx)
			return
		})
		if err == nil {
			ep.vethHost = pair[0]
			ep.addr = addr
			ep.macAddr = pair[1].Attrs().HardwareAddr
			ep.vethCont = pair[1]
			ep.daemon = request.Daemon
		}
		return
	})
	if loaded {
		return status.Error(codes.AlreadyExists, fmt.Sprintf("endpoint %s already exists", request.Id))
	}
	return err
}

func (ts *server) join(ctx context.Context, request *rpc.EndpointIdentifier) (*rpc.JoinResponse, error) {
	ep, loaded := ts.endpoints.Load(request.Id)
	if !loaded {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("endpoint %s not found", request.Id))
	}
	vhn := ep.vethHost.Attrs().Name
	dlog.Debugf(ctx, "link set %s up", vhn)
	err := netlink.LinkSetUp(ep.vethHost)
	if err != nil {
		return nil, fmt.Errorf("link set %s up failed: %w", vhn, err)
	}
	rsp := &rpc.JoinResponse{
		InterfaceSrcName:   ep.vethCont.Attrs().Name,
		InterfaceDstPrefix: "tpd-",
	}
	if !ep.daemon {
		ts.watchersMutex.Lock()
		rs := ts.currentRoutes
		ts.watchersMutex.Unlock()
		rsb := make([][]byte, len(rs))
		for i, r := range rs {
			rsb[i], err = r.MarshalBinary()
			if err != nil {
				return nil, err
			}
		}
		rsp.Routes = rsb
		rsp.Via, _ = ts.daemonAddr.MarshalBinary()
	}
	for _, gw := range ts.gateways {
		if rsp.GwIpV4 == nil && gw.Addr().Is4() {
			rsp.GwIpV4, _ = gw.MarshalBinary()
		}
		if rsp.GwIpV6 == nil && gw.Addr().Is6() {
			rsp.GwIpV6, _ = gw.MarshalBinary()
		}
	}
	return rsp, nil
}

func (ts *server) Leave(_ context.Context, request *rpc.EndpointIdentifier) (*emptypb.Empty, error) {
	_, loaded := ts.endpoints.Load(request.Id)
	if !loaded {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("endpoint %s not found", request.Id))
	}
	return nil, nil
}

func (ts *server) RemoveEndpoint(_ context.Context, request *rpc.EndpointIdentifier) (*emptypb.Empty, error) {
	_, loaded := ts.endpoints.LoadAndDelete(request.Id)
	if !loaded {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("endpoint %s not found", request.Id))
	}
	return &emptypb.Empty{}, nil
}

func (ts *server) bridge() (netlink.Link, error) {
	br, err := netlink.LinkByIndex(ts.bridgeIdx)
	if err != nil {
		err = fmt.Errorf("could not find bridge %q: %w", bridgeName, err)
	}
	return br, err
}

func (ts *server) serve(ctx context.Context) error {
	dlog.Infof(ctx, "Starting service on port %d", ts.port)
	defer dlog.Info(ctx, "Service stopped")

	lc := net.ListenConfig{}
	trListener, err := lc.Listen(ctx, "tcp", fmt.Sprintf(":%d", ts.port))
	if err != nil {
		return err
	}

	var br netlink.Link = &netlink.Bridge{
		LinkAttrs: netlink.LinkAttrs{
			Name: bridgeName,
		},
	}
	dlog.Debugf(ctx, "link add %s", bridgeName)
	err = netlink.LinkAdd(br)
	if err != nil {
		return fmt.Errorf("link add %s failed: %w", bridgeName, err)
	}
	dlog.Debugf(ctx, "link set %s up", bridgeName)
	err = netlink.LinkSetUp(br)
	if err != nil {
		return fmt.Errorf("link set %s up failed: %w", bridgeName, err)
	}
	ts.bridgeIdx = br.Attrs().Index

	go func() {
		for rs := range ts.routesCh {
			ts.watchersMutex.Lock()
			ts.currentRoutes = rs
			ts.watchersMutex.Unlock()
		}
	}()

	// The Connect stream listener needs to know when it's time to leave, so that
	// the grpcServer can perform a graceful shutdown.
	ts.done = ctx.Done()

	svc := grpcServer.New(ctx)
	rpc.RegisterTelerouteServer(svc, ts)
	return grpcServer.Serve(ctx, svc, trListener)
}

// nameRndSize is the length of the random byte slice. It's chosen to fit the
// base32 encoding where each char holds 5 bits.
// Using the formula (srcSize * 8 + 4) / 5, we then get a 12 char string.
const nameRndSize = 7

var smallcapsEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding) //nolint:gochecknoglobals // constant

// generateInterfaceName creates a random 15-character long name consisting of
// the prefix "tp-" and a 12-character long base32 encoded 7-byte random value.
func generateInterfaceName() string {
	n1 := make([]byte, nameRndSize)
	_, _ = rand.Read(n1)
	return "tp-" + smallcapsEncoding.EncodeToString(n1)
}

func createVethPair(ctx context.Context) (pair [2]netlink.Link, err error) {
	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{
			Name:         generateInterfaceName(),
			HardwareAddr: vif.RandomMAC(),
		},
		PeerName:         generateInterfaceName(),
		PeerHardwareAddr: vif.RandomMAC(),
	}

	dlog.Debugf(ctx, "link add veth %s/%s", veth.Attrs().Name, veth.PeerName)
	err = netlink.LinkAdd(veth)
	if err != nil {
		return pair, fmt.Errorf("cannot create veth pair %s/%s: %w", veth.Name, veth.PeerName, err)
	}

	pair[0], err = netlink.LinkByName(veth.Name)
	if err != nil {
		return pair, linkNotFoundError{name: veth.Name, err: err}
	}
	pair[1], err = netlink.LinkByName(veth.PeerName)
	if err != nil {
		return pair, linkNotFoundError{name: veth.PeerName, err: err}
	}
	return pair, nil
}
