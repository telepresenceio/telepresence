package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/netip"
	"strconv"
	"time"

	"github.com/docker/go-plugins-helpers/network"
	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/puzpuzpuz/xsync/v4"
	log "github.com/sirupsen/logrus"
	"github.com/vishvananda/netlink"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/teleroute"
)

type driver struct {
	networks *xsync.Map[string, *networkState]
}

type networkState struct {
	// clientConn is connected to the Telepresence daemon telemount gRPC.
	clientConn *grpc.ClientConn

	// bridge is the bridge that all veth interfaces will connect to.
	bridge netlink.Link

	// hostVeth is this drivers side of the veth pair that connects us to the Telepresence daemon
	hostVeth netlink.Link

	// endpoints contains one vethPair per joined container.
	endpoints map[string]vethPair

	// dnsServer provided by the Telepresence daemon
	dnsServer netip.Addr

	// staticRoutes propagated from the Telepresence daemon
	staticRoutes []*network.StaticRoute
}

func (ns *networkState) callDaemon(f func(ctx context.Context, client teleroute.TelerouteClient) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return f(ctx, teleroute.NewTelerouteClient(ns.clientConn))
}

type vethPair struct {
	host      netlink.Link
	container netlink.Link
}

func New() network.Driver {
	return &driver{
		networks: xsync.NewMap[string, *networkState](),
	}
}

type errNetworkNotFound string

func (e errNetworkNotFound) Error() string {
	return fmt.Sprintf("network %q not found", string(e))
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

func (d *driver) GetCapabilities() (*network.CapabilitiesResponse, error) {
	return &network.CapabilitiesResponse{
		Scope: network.LocalScope,
	}, nil
}

func (d *driver) CreateNetwork(r *network.CreateNetworkRequest) (err error) {
	log.Debugf("CreateNetwork %s, %v", r.NetworkID, r.Options)
	defer func() {
		if err != nil {
			log.Error(err)
		}
	}()

	ns := &networkState{
		endpoints: make(map[string]vethPair),
	}

	var host netip.Addr
	var port uint64
	var pid int

	if gos, ok := r.Options["com.docker.network.generic"].(map[string]any); ok {
		for key, anyVal := range gos {
			val, ok := anyVal.(string)
			if !ok {
				return fmt.Errorf("invalid option value type for %q: %T", key, anyVal)
			}
			switch key {
			case "host":
				host, err = netip.ParseAddr(val)
			case "port":
				if port, err = strconv.ParseUint(val, 10, 16); err == nil {
					if port == 0 {
						err = errors.New(`option "port" cannot be 0`)
					}
				}
			case "pid":
				pid, err = strconv.Atoi(val)
			default:
				err = fmt.Errorf("illegal option %q", key)
			}
			if err != nil {
				return err
			}
		}
	}
	if !host.IsValid() {
		return errors.New(`missing required option "host"`)
	}
	if port == 0 {
		return errors.New(`missing required option "port"`)
	}
	if pid == 0 {
		return errors.New(`missing required option "pid"`)
	}

	ap := netip.AddrPortFrom(host, uint16(port))
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

	err = ns.dnsServer.UnmarshalBinary(info.DnsServer)
	if err != nil {
		return err
	}

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

	defer func() {
		if err != nil {
			_ = netlink.LinkDel(ns.bridge)
		}
	}()

	var pair [2]netlink.Link
	pair, err = createVethPair("dmn")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = netlink.LinkDel(pair[0])
		}
	}()

	ns.hostVeth = pair[0]
	daemonVeth := pair[1]
	dn := daemonVeth.Attrs().Name

	// Move the daemonVeth interface to the daemon's network namespace
	err = netlink.LinkSetNsPid(daemonVeth, pid)
	if err != nil {
		return fmt.Errorf("unable to set netns of veth %q to netns pid %d: %w", dn, pid, err)
	}
	log.Debugf("veth %s moved to pid %d", dn, pid)

	err = ns.callDaemon(func(ctx context.Context, client teleroute.TelerouteClient) error {
		_, err := client.EnslaveVIF(ctx, &teleroute.EnslaveRequest{InterfaceName: dn})
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
		return fmt.Errorf("link up %q: %w", bn, err)
	}

	err = netlink.LinkSetUp(ns.bridge)
	if err != nil {
		return fmt.Errorf("link up %q: %w", bn, err)
	}
	d.networks.Store(r.NetworkID, ns)

	go func() {
		err = d.watchRoutes(r.NetworkID)
		if err != nil {
			log.Error(err)
		}
	}()
	return nil
}

func (d *driver) DeleteNetwork(request *network.DeleteNetworkRequest) error {
	return d.deleteNetworkResources(request.NetworkID)
}

func (d *driver) deleteNetworkResources(networkID string) error {
	n, ok := d.networks.LoadAndDelete(networkID)
	if !ok {
		return errNetworkNotFound(networkID)
	}
	for _, veth := range n.endpoints {
		downAndOut(veth.host)
	}
	downAndOut(n.hostVeth)
	downAndOut(n.bridge)
	_ = n.clientConn.Close()
	return nil
}

func downAndOut(l netlink.Link) {
	err := netlink.LinkDel(l)
	if err != nil {
		log.Errorf("link del %q: %v", l.Attrs().Name, err)
	}
}

func (d *driver) CreateEndpoint(request *network.CreateEndpointRequest) (*network.CreateEndpointResponse, error) {
	return &network.CreateEndpointResponse{}, d.withNetworkUpdate(request.NetworkID, func(ep *networkState) error {
		pair, err := createVethPair("tel")
		if err != nil {
			return err
		}
		vp := vethPair{host: pair[0], container: pair[1]}
		err = netlink.LinkSetUp(vp.host)
		if err != nil {
			return err
		}
		err = netlink.LinkSetMaster(vp.host, ep.bridge)
		if err != nil {
			return err
		}
		ep.endpoints[request.EndpointID] = vp
		return nil
	})
}

func (d *driver) DeleteEndpoint(request *network.DeleteEndpointRequest) error {
	return d.withNetworkUpdate(request.NetworkID, func(n *networkState) error {
		eid := request.EndpointID
		veth, ok := n.endpoints[eid]
		if !ok {
			return fmt.Errorf(`no such endpoint ID "%s"`, eid)
		}
		downAndOut(veth.host)
		delete(n.endpoints, eid)
		return nil
	})
}

func (d *driver) EndpointInfo(request *network.InfoRequest) (*network.InfoResponse, error) {
	vm := make(map[string]string)
	err := d.withEndpoint(request.NetworkID, request.EndpointID, func(n *networkState, vp vethPair) error {
		vm["io.telepresence.teleroute.dnsserver"] = n.dnsServer.String()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &network.InfoResponse{Value: vm}, nil
}

func (d *driver) watchRoutes(networkID string) error {
	n, ok := d.networks.Load(networkID)
	if !ok {
		return errNetworkNotFound(networkID)
	}

	defer func() {
		// deleting the network would require access to the docker daemon, which we don't have. So
		// we just free up the resources here by deleting our bridge and all veths.
		err := d.deleteNetworkResources(networkID)
		if err != nil {
			log.Errorf("error deleting network %s: %v", networkID, err)
		}
	}()

	stream, err := teleroute.NewTelerouteClient(n.clientConn).WatchRoutes(context.Background(), &emptypb.Empty{})
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
		err = d.withNetworkUpdate(networkID, func(n *networkState) error {
			n.staticRoutes = srs
			return nil
		})
		if err != nil {
			return err
		}
	}
}

func (d *driver) Join(request *network.JoinRequest) (*network.JoinResponse, error) {
	var ifn string
	var srs []*network.StaticRoute
	err := d.withEndpoint(request.NetworkID, request.EndpointID, func(n *networkState, vp vethPair) error {
		ifn = vp.container.Attrs().Name
		srs = n.staticRoutes
		return nil
	})
	if err != nil {
		log.Error(err)
		return nil, err
	}

	rsp := &network.JoinResponse{
		InterfaceName: network.InterfaceName{
			SrcName:   ifn,
			DstPrefix: "eth",
		},
		DisableGatewayService: true,
		StaticRoutes:          srs,
	}
	if log.GetLevel() >= log.DebugLevel {
		jr, _ := json.Marshal(rsp, jsontext.WithIndent("  "))
		log.Debugf(string(jr))
	}
	return rsp, nil
}

func (d *driver) Leave(_ *network.LeaveRequest) (err error) {
	return nil
}

func (d *driver) DiscoverNew(*network.DiscoveryNotification) error {
	return nil
}

func (d *driver) DiscoverDelete(*network.DiscoveryNotification) error {
	return nil
}

func (d *driver) ProgramExternalConnectivity(*network.ProgramExternalConnectivityRequest) error {
	return nil
}

func (d *driver) RevokeExternalConnectivity(*network.RevokeExternalConnectivityRequest) error {
	return nil
}

func (d *driver) AllocateNetwork(*network.AllocateNetworkRequest) (*network.AllocateNetworkResponse, error) {
	return nil, nil
}

func (d *driver) FreeNetwork(*network.FreeNetworkRequest) error {
	return nil
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

func (d *driver) withNetwork(id string, f func(*networkState) error) (err error) {
	n, ok := d.networks.Load(id)
	if !ok {
		return errNetworkNotFound(id)
	}
	return f(n)
}

func (d *driver) withNetworkUpdate(id string, f func(*networkState) error) (err error) {
	d.networks.Compute(id, func(n *networkState, loaded bool) (*networkState, xsync.ComputeOp) {
		op := xsync.CancelOp
		if loaded {
			err = f(n)
			if err == nil {
				op = xsync.UpdateOp
			}
		} else {
			err = errNetworkNotFound(id)
		}
		return n, op
	})
	return
}

func (d *driver) withEndpoint(networkID, endpointID string, f func(ep *networkState, vp vethPair) error) (err error) {
	return d.withNetwork(networkID, func(ep *networkState) error {
		veth, ok := ep.endpoints[endpointID]
		if !ok {
			return fmt.Errorf(`no such endpoint ID "%s"`, endpointID)
		}
		return f(ep, veth)
	})
}
