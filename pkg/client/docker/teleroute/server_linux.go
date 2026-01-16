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
	"strings"
	"sync"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/vishvananda/netlink"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/clog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/teleroute"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	grpcServer "github.com/telepresenceio/telepresence/v2/pkg/grpc/server"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
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
	macAddr  net.HardwareAddr
	vethCont netlink.Link
	vethHost netlink.Link
	daemon   bool
}

type server struct {
	rpc.UnsafeTelerouteServer
	done           <-chan struct{}
	watchersMutex  sync.Mutex
	pluginPid      int
	routesCh       <-chan []netip.Prefix
	currentRoutes  []netip.Prefix
	gateways       []netip.Prefix
	tap            *vif.TunnelingDevice
	bridgeIdx      int
	endpoints      *xsync.Map[string, endpoint]
	endpointCache  *xsync.Map[netip.Addr, [2]netlink.Link]
	port           uint16
	daemonAddrIPv4 netip.Addr
	daemonAddrIPv6 netip.Addr
}

func StartServer(g log.Group, tap *vif.TunnelingDevice, routesCh <-chan []netip.Prefix, teleroutePort uint16) (Server, error) {
	ts := &server{
		routesCh:      routesCh,
		done:          make(chan struct{}),
		tap:           tap,
		port:          teleroutePort,
		endpoints:     xsync.NewMap[string, endpoint](),
		endpointCache: xsync.NewMap[netip.Addr, [2]netlink.Link](),
	}
	g.Go("teleroute", ts.serve)
	return ts, nil
}

func (ts *server) DaemonAddresses() []netip.Addr {
	addrs := make([]netip.Addr, 0, 2)
	if ts.daemonAddrIPv4.IsValid() {
		addrs = append(addrs, ts.daemonAddrIPv4)
	}
	if ts.daemonAddrIPv6.IsValid() {
		addrs = append(addrs, ts.daemonAddrIPv6)
	}
	return addrs
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
	if err != nil && status.Code(err) == codes.Unknown {
		err = status.Error(codes.Internal, err.Error())
	}
	return &emptypb.Empty{}, err
}

func (ts *server) Join(ctx context.Context, request *rpc.EndpointIdentifier) (*rpc.JoinResponse, error) {
	rsp, err := ts.join(ctx, request)
	if err != nil && status.Code(err) == codes.Unknown {
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
	clog.Debugf(ctx, "link set %s master %s", vhn, brn)
	err = netlink.LinkSetMaster(pair[0], br)
	if err != nil {
		return pair, fmt.Errorf("link set %s master %s failed: %w", vhn, brn, err)
	}
	vcn := pair[1].Attrs().Name
	clog.Debugf(ctx, "link set %s netns %d", vcn, ts.pluginPid)
	err = netlink.LinkSetNsPid(pair[1], ts.pluginPid)
	if err != nil {
		return pair, fmt.Errorf("link set %s netns %d failed: %w", vcn, ts.pluginPid, err)
	}
	return pair, err
}

func addrFromRaw(raw []byte) (netip.Addr, error) {
	if len(raw) == 0 {
		return netip.Addr{}, nil
	}
	var ip netip.Addr
	if err := ip.UnmarshalBinary(raw); err != nil {
		return netip.Addr{}, err
	}
	return ip, nil
}

func (ts *server) createEndpoint(ctx context.Context, request *rpc.CreateEndpointRequest) error {
	keyAddr := netip.Addr{}
	addrIPv4, err := addrFromRaw(request.AddrIpv4)
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	addrIPv6, err := addrFromRaw(request.AddrIpv6)
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	if request.Daemon {
		ts.daemonAddrIPv4 = addrIPv4
		ts.daemonAddrIPv6 = addrIPv6
	}
	switch {
	case addrIPv4.IsValid():
		keyAddr = addrIPv4
	case addrIPv6.IsValid():
		keyAddr = addrIPv6
	default:
		return status.Error(codes.InvalidArgument, "neither IPv4 nor IPv6 address provided")
	}
	_, loaded := ts.endpoints.LoadOrCompute(request.Id, func() (ep endpoint, cancel bool) {
		pair, _ := ts.endpointCache.LoadOrCompute(keyAddr, func() (pair [2]netlink.Link, cancel bool) {
			pair, err = ts.createAddressEndpoint(ctx)
			return pair, err != nil
		})
		if err == nil {
			ep.vethHost = pair[0]
			ep.macAddr = pair[1].Attrs().HardwareAddr
			ep.vethCont = pair[1]
			ep.daemon = request.Daemon
		}
		return ep, false
	})
	if loaded {
		return status.Error(codes.AlreadyExists, fmt.Sprintf("endpoint %s already exists", request.Id))
	}
	return err
}

type responseStringer struct {
	*rpc.JoinResponse
}

func writeRawIP(raw []byte, w *strings.Builder) {
	if len(raw) == 0 {
		w.WriteString("nil")
		return
	}
	var ip netip.Addr
	if err := ip.UnmarshalBinary(raw); err == nil {
		w.WriteString(ip.String())
	} else {
		_, _ = fmt.Fprintf(w, "(%#v: error %v)", raw, err)
	}
}

func writeRawPrefix(raw []byte, w *strings.Builder) {
	if len(raw) == 0 {
		w.WriteString("nil")
		return
	}
	var pfx netip.Prefix
	if err := pfx.UnmarshalBinary(raw); err == nil {
		w.WriteString(pfx.String())
	} else {
		_, _ = fmt.Fprintf(w, "(%#v: error %v)", raw, err)
	}
}

func (r responseStringer) String() string {
	bld := &strings.Builder{}
	bld.WriteString("JoinResponse{InterfaceSrcName: ")
	bld.WriteString(r.InterfaceSrcName)
	bld.WriteString(", InterfaceDstPrefix: ")
	bld.WriteString(r.InterfaceDstPrefix)
	bld.WriteString(", GwIpV4: ")
	writeRawPrefix(r.GwIpV4, bld)
	bld.WriteString(", GwIpV6: ")
	writeRawPrefix(r.GwIpV6, bld)
	bld.WriteString(", routes: [")
	for i, r := range r.Routes {
		if i > 0 {
			bld.WriteString(", ")
		}
		writeRawPrefix(r, bld)
	}
	bld.WriteString("], via: ")
	writeRawIP(r.Via, bld)
	bld.WriteString("}")
	return bld.String()
}

func (ts *server) isIPv6() bool {
	ts.watchersMutex.Lock()
	rs := ts.currentRoutes
	ts.watchersMutex.Unlock()
	for _, r := range rs {
		if r.Addr().Is6() {
			return true
		}
	}
	return false
}

func (ts *server) join(ctx context.Context, request *rpc.EndpointIdentifier) (*rpc.JoinResponse, error) {
	ep, loaded := ts.endpoints.Load(request.Id)
	if !loaded {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("endpoint %s not found", request.Id))
	}
	vhn := ep.vethHost.Attrs().Name
	clog.Debugf(ctx, "link set %s up", vhn)
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
		if ts.isIPv6() {
			if !ts.daemonAddrIPv6.IsValid() {
				return nil, status.Error(codes.Internal, "IPv6 is not enabled for the teleroute network")
			}
			rsp.Via, _ = ts.daemonAddrIPv6.MarshalBinary()
		} else {
			if !ts.daemonAddrIPv4.IsValid() {
				return nil, status.Error(codes.Internal, "IPv4 is not enabled for the teleroute network")
			}
			rsp.Via, _ = ts.daemonAddrIPv4.MarshalBinary()
		}
	}
	for _, gw := range ts.gateways {
		if rsp.GwIpV4 == nil && gw.Addr().Is4() {
			rsp.GwIpV4, _ = gw.MarshalBinary()
		}
		if rsp.GwIpV6 == nil && gw.Addr().Is6() {
			rsp.GwIpV6, _ = gw.MarshalBinary()
		}
	}
	clog.Debug(ctx, responseStringer{JoinResponse: rsp})
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
	clog.Infof(ctx, "Starting service on port %d", ts.port)
	defer clog.Info(ctx, "Service stopped")

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
	clog.Debugf(ctx, "link add %s", bridgeName)
	err = netlink.LinkAdd(br)
	if err != nil {
		return fmt.Errorf("link add %s failed: %w", bridgeName, err)
	}
	clog.Debugf(ctx, "link set %s up", bridgeName)
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

	clog.Debugf(ctx, "link add veth %s/%s", veth.Attrs().Name, veth.PeerName)
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
