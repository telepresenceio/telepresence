// Package teleroute contains the Telepresence Daemon Teleroute service that the Docker Network Plugin with the
// same name connects to.
package teleroute

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"sync"

	"github.com/vishvananda/netlink"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/teleroute"
	grpcServer "github.com/telepresenceio/telepresence/v2/pkg/grpc/server"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

type server struct {
	rpc.UnsafeTelerouteServer
	watchersMutex sync.Mutex
	watchersIndex int
	watchers      map[int]chan<- []netip.Prefix
	routesCh      <-chan []netip.Prefix
	currentRoutes []netip.Prefix
	dnsIP         netip.Addr
	vif           netlink.Link
	port          uint16
}

type Server interface {
	Serve(ctx context.Context) error
}

func NewServer(routesCh <-chan []netip.Prefix, dnsIP netip.Addr, vif netlink.Link, teleroutePort uint16) Server {
	return &server{
		routesCh: routesCh,
		dnsIP:    dnsIP,
		vif:      vif,
		port:     teleroutePort,
		watchers: make(map[int]chan<- []netip.Prefix),
	}
}

func (ts *server) Serve(ctx context.Context) error {
	dlog.Infof(ctx, "Starting service on port %d", ts.port)
	defer dlog.Info(ctx, "Service stopped")
	lc := net.ListenConfig{}
	trListener, err := lc.Listen(ctx, "tcp", fmt.Sprintf(":%d", ts.port))
	if err != nil {
		return err
	}
	svc := grpcServer.New(ctx)
	rpc.RegisterTelerouteServer(svc, ts)

	go func() {
		// Continuously dispatch routes to all watchers.
		for rs := range ts.routesCh {
			ts.watchersMutex.Lock()
			if !slices.Equal(ts.currentRoutes, rs) {
				dlog.Debugf(ctx, "Dispatching %d routes to %d watchers", len(rs), len(ts.watchers))
				ts.currentRoutes = rs
				for _, watcher := range ts.watchers {
					watcher <- rs
				}
			}
			ts.watchersMutex.Unlock()
		}
		ts.watchersMutex.Lock()
		dlog.Debugf(ctx, "Closing %d watchers", len(ts.watchers))
		for _, watcher := range ts.watchers {
			close(watcher)
		}
		ts.watchersMutex.Unlock()
	}()

	err = grpcServer.Serve(ctx, svc, trListener)
	if err != nil {
		return err
	}
	return nil
}

func (ts *server) Info(context.Context, *emptypb.Empty) (*rpc.InfoResponse, error) {
	dnsServer, err := ts.dnsIP.MarshalBinary()
	if err != nil {
		return nil, err
	}
	return &rpc.InfoResponse{
		Version:   version.Structured.String(),
		DnsServer: dnsServer,
	}, nil
}

func (ts *server) EnslaveVIF(ctx context.Context, request *rpc.EnslaveRequest) (r *emptypb.Empty, err error) {
	defer func() {
		if err != nil {
			err = status.Error(codes.Internal, err.Error())
		}
	}()

	lookup := func(name string) (netlink.Link, error) {
		l, err := netlink.LinkByName(name)
		if err != nil {
			err = fmt.Errorf("failed to get link %q by name: %v", name, err)
			dlog.Error(ctx, err)
		}
		return l, err
	}

	setMaster := func(slave, master netlink.Link) error {
		err := netlink.LinkSetMaster(slave, master)
		if err != nil {
			err = fmt.Errorf("failed to set link %s master %s: %v", slave.Attrs().Name, master.Attrs().Name, err)
			dlog.Error(ctx, err)
		}
		return err
	}

	setUp := func(l netlink.Link) error {
		err := netlink.LinkSetUp(l)
		if err != nil {
			err = fmt.Errorf("failed to set link %s up: %v", l.Attrs().Name, err)
			dlog.Error(ctx, err)
		}
		return err
	}

	brName := "br-daemon"
	var br netlink.Link = &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: brName}}
	err = netlink.LinkAdd(br)
	if err != nil {
		return nil, fmt.Errorf("failed to add bridge %q: %v", brName, err)
	}
	br, err = lookup(brName)
	if err != nil {
		return nil, err
	}
	vifVeth, err := lookup(request.InterfaceName)
	if err != nil {
		return nil, err
	}
	err = setMaster(ts.vif, br)
	if err != nil {
		return nil, err
	}
	err = setMaster(vifVeth, br)
	if err != nil {
		return nil, err
	}
	err = setUp(br)
	if err != nil {
		return nil, err
	}
	err = setUp(vifVeth)
	if err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (ts *server) addRouteSubscriber(ch chan<- []netip.Prefix) (id int) {
	ts.watchersMutex.Lock()
	id = ts.watchersIndex
	ts.watchers[id] = ch
	ts.watchersIndex++
	ch <- ts.currentRoutes // Post initial update
	ts.watchersMutex.Unlock()
	return id
}

func (ts *server) removeRouteSubscriber(ix int) {
	ts.watchersMutex.Lock()
	delete(ts.watchers, ix)
	ts.watchersMutex.Unlock()
}

func (ts *server) WatchRoutes(_ *emptypb.Empty, stream rpc.Teleroute_WatchRoutesServer) (err error) {
	routesCh := make(chan []netip.Prefix, 2)
	wix := ts.addRouteSubscriber(routesCh)
	defer ts.removeRouteSubscriber(wix)

	for curSubnets := range routesCh {
		rs := make([][]byte, len(curSubnets))
		for i, r := range curSubnets {
			rs[i], err = r.MarshalBinary()
			if err != nil {
				return err
			}
		}
		err = stream.Send(&rpc.Routes{Routes: rs})
		if err != nil {
			return err
		}
	}
	return nil
}
