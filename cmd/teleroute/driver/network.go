package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/docker/go-plugins-helpers/network"
	"github.com/puzpuzpuz/xsync/v4"
	log "github.com/sirupsen/logrus"
	"github.com/vishvananda/netlink"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/teleroute"
)

type networkState struct {
	options

	// initial is true until the first container (the daemon) has joined the network.
	initial atomic.Bool

	// clientConn is connected to the Telepresence daemon telemount gRPC.
	clientConn *grpc.ClientConn

	// bridge is the bridge that all veth interfaces will connect to.
	bridge netlink.Link

	// hostVeth is this drivers side of the veth pair that connects us to the VIF Telepresence daemon.
	hostVeth netlink.Link

	// daemonVethName name hostVeth peer that lives in the daemon.
	daemonVethName string

	// endpoints contains one vethPair per joined container.
	endpoints *xsync.Map[string, vethPair]

	// dnsServer provided by the Telepresence daemon
	dnsServer netip.Addr

	// staticRoutes propagated from the Telepresence daemon
	staticRoutes []*network.StaticRoute

	// concurrency protection for the staticRoutes
	staticRoutesMutex sync.Mutex
}

func newNetworkState() *networkState {
	ns := &networkState{
		endpoints: xsync.NewMap[string, vethPair](),
	}
	ns.initial.Store(true)
	return ns
}

func (ns *networkState) initialize() error {
	err := ns.connectToDaemon()
	if err != nil {
		return err
	}
	err = ns.createBridge()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = netlink.LinkDel(ns.bridge)
		}
	}()
	err = ns.createVethPair()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = netlink.LinkDel(ns.hostVeth)
		}
	}()

	err = ns.callDaemon(func(ctx context.Context, client teleroute.TelerouteClient) error {
		_, err := client.EnslaveVIF(ctx, &teleroute.EnslaveRequest{InterfaceName: ns.daemonVethName})
		return err
	})
	if err != nil {
		return err
	}

	err = netlink.LinkSetMaster(ns.hostVeth, ns.bridge)
	if err != nil {
		return fmt.Errorf("unable to set bridge %q as master of veth %q: %w", ns.bridge.Attrs().Name, ns.hostVeth.Attrs().Name, err)
	}

	err = netlink.LinkSetUp(ns.hostVeth)
	if err != nil {
		return fmt.Errorf("link up %q: %w", ns.bridge, err)
	}
	err = netlink.LinkSetUp(ns.bridge)
	if err != nil {
		return fmt.Errorf("link up %q: %w", ns.bridge, err)
	}
	return nil
}

type vethPair struct {
	host      netlink.Link
	container netlink.Link
}

func (ns *networkState) connectToDaemon() (err error) {
	ap := netip.AddrPortFrom(ns.host, ns.port)
	ns.clientConn, err = grpc.NewClient(ap.String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			ns.clientConn.Close()
		}
	}()

	var info *teleroute.InfoResponse
	err = ns.callDaemon(func(ctx context.Context, client teleroute.TelerouteClient) error {
		info, err = client.Info(ctx, &emptypb.Empty{})
		return err
	})
	if err != nil {
		return err
	}
	log.Debugf("connected to Telepresence daemon, version %s", info.Version)
	return nil
}

func (ns *networkState) callDaemon(f func(ctx context.Context, client teleroute.TelerouteClient) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return f(ctx, teleroute.NewTelerouteClient(ns.clientConn))
}

type linkNotFound struct {
	name string
	err  error
}

func (e linkNotFound) Error() string {
	return fmt.Sprintf("link %q not found: %s", e.name, e.err.Error())
}

func (e linkNotFound) Unwrap() error {
	return e.err
}

func (ns *networkState) createBridge() (err error) {
	bn := fmt.Sprintf("tel-%08x", rand.Int31())
	err = netlink.LinkAdd(&netlink.Bridge{
		LinkAttrs: netlink.LinkAttrs{
			Name: bn,
		},
	})
	if err != nil {
		return fmt.Errorf("cannot create bridge %s: %w", bn, err)
	}

	ns.bridge, err = netlink.LinkByName(bn)
	if err != nil {
		return linkNotFound{name: bn, err: err}
	}
	return nil
}

func (ns *networkState) createVethPair() (err error) {
	var vp [2]netlink.Link
	vp, err = createVethPair("vif")
	if err != nil {
		return err
	}
	ns.hostVeth = vp[0]
	daemonVeth := vp[1]

	// Move the daemonVeth interface to the daemon's network namespace where it will be bridged to its VIF
	ns.daemonVethName = daemonVeth.Attrs().Name
	err = netlink.LinkSetNsPid(daemonVeth, ns.pid)
	if err != nil {
		_ = netlink.LinkDel(ns.hostVeth)
		return fmt.Errorf("unable to set netns of veth %q to netns pid %d: %w", ns.daemonVethName, ns.pid, err)
	}
	log.Debugf("veth %s moved to pid %d", ns.daemonVethName, ns.pid)
	return nil
}

func (ns *networkState) createEndpoint(endpointID string) error {
	pair, err := createVethPair("tel")
	if err != nil {
		return err
	}
	vp := vethPair{host: pair[0], container: pair[1]}
	err = netlink.LinkSetUp(vp.host)
	if err != nil {
		return err
	}
	err = netlink.LinkSetMaster(vp.host, ns.bridge)
	if err != nil {
		return err
	}
	ns.endpoints.Store(endpointID, vp)
	return nil
}

func (ns *networkState) deleteEndpoint(eid string) error {
	veth, ok := ns.endpoints.LoadAndDelete(eid)
	if !ok {
		return fmt.Errorf(`no such endpoint ID "%s"`, eid)
	}
	downAndOut(veth.host)
	return nil
}

func (ns *networkState) getStaticRoutes() (srs []*network.StaticRoute) {
	ns.staticRoutesMutex.Lock()
	if l := len(ns.staticRoutes); l > 0 {
		srs = make([]*network.StaticRoute, l)
		copy(srs, ns.staticRoutes)
	}
	ns.staticRoutesMutex.Unlock()
	return srs
}

func (ns *networkState) setStaticRoutes(srs []*network.StaticRoute) {
	ns.staticRoutesMutex.Lock()
	if l := len(srs); l > 0 {
		ns.staticRoutes = make([]*network.StaticRoute, l)
		copy(ns.staticRoutes, srs)
	} else {
		ns.staticRoutes = nil
	}
	ns.staticRoutesMutex.Unlock()
}

func (ns *networkState) join(endpointID string) (ifn network.InterfaceName, srs []*network.StaticRoute, err error) {
	vp, ok := ns.endpoints.Load(endpointID)
	if !ok {
		return ifn, nil, fmt.Errorf(`no such endpoint ID "%s"`, endpointID)
	}
	ifn = network.InterfaceName{
		SrcName:   vp.container.Attrs().Name,
		DstPrefix: ns.ifPrefix,
	}
	if ns.initial.CompareAndSwap(true, false) {
		// The daemon container must be connected to be able to route intercepted traffic to other containers
		// that are connected. The Telepresence CLI connects it just after the creation of the network.
		//
		// The daemon is the provider of the static routes, and docker will add the routes provided here, so
		// an empty list must be used to avoid route collisions.
		srs = []*network.StaticRoute{}
		ifn.DstPrefix = "trn"
	} else {
		srs = ns.getStaticRoutes()
	}
	return ifn, srs, nil
}

func (ns *networkState) deleteResources() error {
	ns.endpoints.Range(func(key string, veth vethPair) bool {
		downAndOut(veth.host)
		return true
	})
	downAndOut(ns.hostVeth)
	downAndOut(ns.bridge)
	_ = ns.clientConn.Close()
	return nil
}

func downAndOut(l netlink.Link) {
	err := netlink.LinkDel(l)
	if err != nil {
		log.Errorf("link del %q: %v", l.Attrs().Name, err)
	}
}

func (ns *networkState) watchRoutes() error {
	stream, err := teleroute.NewTelerouteClient(ns.clientConn).WatchRoutes(context.Background(), &emptypb.Empty{})
	if err != nil {
		return fmt.Errorf("failed to watch daemon routes: %v", err)
	}

	for {
		rts, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = nil
			}
			return err
		}
		srs := make([]*network.StaticRoute, len(rts.Routes))
		for i, rt := range rts.Routes {
			p := netip.Prefix{}
			err = p.UnmarshalBinary(rt)
			if err != nil {
				return fmt.Errorf("failed to unmarshal daemon route: %v", err)
			}
			srs[i] = &network.StaticRoute{Destination: p.String(), RouteType: 1}
		}
		ns.setStaticRoutes(srs)
	}
}

func createVethPair(prefix string) (pair [2]netlink.Link, err error) {
	vethName := func() string {
		return fmt.Sprintf("%s-%08x", prefix, rand.Int31())
	}
	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{
			Name: vethName(),
		},
		PeerName: vethName(),
	}

	err = netlink.LinkAdd(veth)
	if err != nil {
		return pair, fmt.Errorf("cannot create veth pair %s/%s: %w", veth.Name, veth.PeerName, err)
	}

	pair[0], err = netlink.LinkByName(veth.Name)
	if err != nil {
		return pair, linkNotFound{name: veth.Name, err: err}
	}
	pair[1], err = netlink.LinkByName(veth.PeerName)
	if err != nil {
		return pair, linkNotFound{name: veth.PeerName, err: err}
	}
	return pair, nil
}
