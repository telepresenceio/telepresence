package rootd

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/netip"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blang/semver/v4"
	dns2 "github.com/miekg/dns"
	"github.com/puzpuzpuz/xsync/v4"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/dlib/v2/dgroup"
	"github.com/telepresenceio/dlib/v2/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/agentpf"
	"github.com/telepresenceio/telepresence/v2/pkg/client/bwcompat"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker/teleroute"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/client/portforward"
	"github.com/telepresenceio/telepresence/v2/pkg/client/rootd/dns"
	"github.com/telepresenceio/telepresence/v2/pkg/client/rootd/vip"
	"github.com/telepresenceio/telepresence/v2/pkg/dnsproxy"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	grpcErrors "github.com/telepresenceio/telepresence/v2/pkg/grpc/errors"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/watcher"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/json"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/slice"
	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
	"github.com/telepresenceio/telepresence/v2/pkg/vif"
)

type agentSubnet struct {
	netip.Prefix
	workload string
}

type agentVIP struct {
	workload      string
	destinationIP netip.Addr
}

// session resolves DNS names and routes outbound traffic that is centered around a TUN device. The router is
// similar to a TUN-to-SOCKS5 but uses a bidirectional gRPC muxTunnel instead of SOCKS when communicating with the
// traffic-manager. The addresses of the device are derived from IP addresses sent to it from the user
// daemon (which in turn receives them from the cluster).
//
// Data sent to the device is received as L3 IP-packets and parsed into L4 UDP and TCP before they
// are dispatched over the muxTunnel. Returned payloads are wrapped as IP-packets before written
// back to the device. This L3 <=> L4 conversation is made using gvisor.dev/gvisor/pkg/tcpip.
//
// Connection pooling:
//
// For UDP and TCP packets, a ConnID is created which uniquely identifies a combination of protocol,
// source IP, source port, destination IP, and destination port. A handler is then obtained that matches
// that ID (active handlers are cached in a tunnel.Pool) and the packet is then sent to that handler.
// The handler typically sends the ConnID and the payload of the packet over to the traffic-manager
// using the gRPC ClientTunnel. At the receiving en din the traffic-manager, a similar tunnel.Pool obtains
// a corresponding handler which manages a net.Conn matching the ConnID in the cluster.
//
// A zero session is invalid; you must use the createSession or the newSession function.
type session struct {
	*k8s.Cluster

	tunVif *vif.TunnelingDevice

	teleroute teleroute.Server

	// managerConn is the connection to the traffic-manager.
	managerConn *grpc.ClientConn

	// agentClients provides the gRPC tunnel to traffic-agents in the connected namespace
	agentClients agentpf.Clients

	// managerVersion is the version of the connected traffic-manager
	managerVersion semver.Version

	// connPool contains handlers that represent active connections. Those handlers
	// are obtained using a connpool.ConnID.
	handlers *tunnel.Pool

	// The local dns server
	dnsServer *dns.Server

	// vifDNS is the address and port of the DNS server attached to the TUN device. This is currently only
	// used in conjunction with systemd-resolved. The current macOS and the overriding solution
	// will dispatch directly to the local DNS Service without going through the TUN device, but
	// that may change later if we decide to dispatch to the DNS-server in the cluster.
	vifDNS netip.AddrPort

	// localDNS is the address and port of the local DNS Service.
	localDNS netip.AddrPort

	// serviceSubnets reported by the traffic-manager
	serviceSubnets []netip.Prefix

	// podSubnets reported by the traffic-manager
	podSubnets []netip.Prefix

	// Subnets configured by the user
	alsoProxySubnets []netip.Prefix

	// Subnets configured by the user to never be proxied
	neverProxySubnets []netip.Prefix

	// Like neverProxySubnets but stripped from the ones that aren't proxied anyway
	effectiveNeverProxy []netip.Prefix

	// Subnets that will be mapped even if they conflict with local routes
	allowConflictingSubnets []netip.Prefix

	// localTranslationTable maps an IP returned by the cluster's DNS to a virtual IP created by this server.
	localTranslationTable *xsync.Map[netip.Addr, netip.Addr]

	// IP addresses that the cluster's DNS resolves that are contained in one of the subnets in this
	// slice are translated to a virtual IP (cached in the localTranslationTable)
	localTranslationSubnets []agentSubnet

	// virtualIPs maps a virtual IP to an agent tunnel.
	virtualIPs *xsync.Map[netip.Addr, agentVIP]

	// vipGenerator generates virtual IPs for a given range.
	vipGenerator vip.Generator

	// closing is set during shutdown and can have the values:
	//   0 = running
	//   1 = closing
	//   2 = closed
	closing int32

	// session contains the manager session
	session *manager.SessionInfo

	// rndSource is the source for the random number generator in the TCP handlers
	rndSource rand.Source

	// Telemetry counters for DNS lookups
	dnsLookups  int
	dnsFailures int

	// Whether pods should be proxied by the TUN-device
	proxyClusterPods bool

	// Whether services should be proxied by the TUN-device
	proxyClusterSvcs bool

	// dnsServerSubnet is normally never set. It is only used when neither proxyClusterPods nor the
	// proxyClusterSvcs are set. In this situation, the VIF would be left without a primary subnet, so
	// it will instead route very small subnet with 30 bit mask, large enough to hold:
	//
	//   n.n.n.0 The IP identifying the subnet
	//   n.n.n.1 The IP of the (non existent) gateway
	//   n.n.n.2 The IP of the DNS server
	//   n.n.n.3 Unused
	//
	// The subnet is guaranteed to be free from all other routed subnets.
	//
	// NOTE: On macOS, where DNS is controlled by adding entries in /etc/resolver that points directly
	// to a port on localhost, there's no need for this subnet.
	dnsServerSubnet netip.Prefix

	// vifReady is closed when the virtual network interface has been configured.
	vifReady chan error

	subnetViaWorkloads []*rpc.SubnetViaWorkload

	// daemon runs as part of a pod-daemon setup.
	podDaemon bool
	routesCh  chan []netip.Prefix

	// Maps one UDP or TCP AddrPort to another
	l4PortMap *xsync.Map[types.AddrPortProto, uint16]

	lookupSequencer *xsync.Map[string, clusterLookupResult]
}

// createSession will establish a connection to the traffic-manager and return a new properly initialized session object.
func createSession(sessionCtx, dialCtx context.Context, mi *rpc.NetworkConfig) (s *session, err error) {
	dlog.Info(sessionCtx, "-- Starting new session")
	kc, err := k8s.NewKubeconfig(sessionCtx, true, mi.KubeFlags, mi.ManagerNamespace, mi.KubeconfigData)
	if err != nil {
		return nil, err
	}
	cl, err := k8s.NewCluster(kc, mi.MappedNamespaces)
	if err != nil {
		return nil, err
	}
	conn, _, ver, err := cl.ConnectToManager(dialCtx, mi.ManagerNamespace)
	if err != nil {
		return nil, err
	}
	return newSession(cl, mi, conn, ver, false)
}

func nope() bool { return false }

func newSession(cluster *k8s.Cluster, mi *rpc.NetworkConfig, managerConn *grpc.ClientConn, ver semver.Version, isPodDaemon bool) (*session, error) {
	dlog.Debugf(cluster, "Creating session with id %v", mi.Session)

	s := &session{
		Cluster:               cluster,
		handlers:              tunnel.NewPool(),
		rndSource:             rand.NewSource(time.Now().UnixNano()),
		session:               mi.Session,
		managerConn:           managerConn,
		managerVersion:        ver,
		subnetViaWorkloads:    mi.SubnetViaWorkloads,
		proxyClusterPods:      true,
		proxyClusterSvcs:      true,
		vifReady:              make(chan error, 2),
		routesCh:              make(chan []netip.Prefix, 2),
		podDaemon:             isPodDaemon,
		localTranslationTable: xsync.NewMap[netip.Addr, netip.Addr](),
		virtualIPs:            xsync.NewMap[netip.Addr, agentVIP](),
		l4PortMap:             xsync.NewMap[types.AddrPortProto, uint16](),
	}
	cfg := client.GetConfig(s)

	// Use simple lookups unless the traffic-manager version is less than 2.25.0 (not supported), or if the user has explicitly
	// requested complex lookups. The presence of the s.lookupSequencer will trigger simple lookups.
	if !(cfg.DNS().UseComplexLookup || semver.MustParse(ver.FinalizeVersion()).LT(semver.MustParse("2.25.0"))) {
		s.lookupSequencer = xsync.NewMap[string, clusterLookupResult]()
		go s.lookupSequencerGC()
	}

	rt := cfg.Routing()
	var err error
	s.alsoProxySubnets, err = validateSubnets("also-proxy", rt.AlsoProxy, s.alsoProxyVia)
	if err != nil {
		return nil, err
	}
	dlog.Infof(s, "also-proxy subnets %v", s.alsoProxySubnets)

	s.neverProxySubnets, err = validateSubnets("never-proxy", rt.NeverProxy, nope)
	if err != nil {
		return nil, err
	}
	dlog.Infof(s, "never-proxy subnets %v", s.neverProxySubnets)

	s.allowConflictingSubnets, err = validateSubnets("allow-conflicting", rt.AllowConflicting, nope)
	if err != nil {
		return nil, err
	}
	dlog.Infof(s, "allow-conflicting subnets %v", s.allowConflictingSubnets)

	s.dnsServer = dns.NewServer(cfg.DNS(), s.Namespace, s.clusterLookup)
	s.SetTopLevelDomains(nil)

	// Set ourselves as the default dialer for the session.
	s.Context = tunnel.WithDialer(s.Context, s)

	// Terminate the routes watcher
	go func() {
		<-s.Done()
		close(s.routesCh)
	}()
	return s, nil
}

// lookupSequencerTTL is the maximum time to keep a lookup result cached with the purpose of avoiding
// both A and AAAA lookups for the same name.
const lookupSequencerTTL = 500 * time.Millisecond

func (s *session) managerClient() manager.ManagerClient {
	return manager.NewManagerClient(s.managerConn)
}

func (s *session) lookupSequencerGC() {
	// Cleans the lookupSequencer from time to time to avoid that it grows too big if many different
	// names are looked up.
	maps.GC(s.lookupSequencer, lookupSequencerTTL, s.Done(), func(key string, value clusterLookupResult) bool {
		return time.Since(value.created) > lookupSequencerTTL
	})
}

func (s *session) resolvePort(ctx context.Context, host, portStr string) (ap types.AddrPortProto, err error) {
	ix := strings.LastIndexByte(portStr, types.ProtoSeparator)
	proto := types.ProtoTCP
	if ix > 0 {
		proto, err = types.ParseProto(portStr[ix+1:])
		if err != nil {
			return ap, err
		}
		portStr = portStr[:ix]
	}

	if port, err := types.ParsePort(portStr); err == nil {
		ip, err := netip.ParseAddr(host)
		if err != nil {
			ip, err = dns.LookupIP(ctx, s.localDNS, dns2.Fqdn(host))
			if err != nil {
				return ap, err
			}
		}
		return types.AddrPortProto{AddrPort: netip.AddrPortFrom(ip, port), Proto: proto}, nil
	}

	// The toPort is symbolic, so it must be resolved using the Kubernetes API.
	_, err = netip.ParseAddr(host)
	if err == nil {
		return ap, errors.New("a symbolic port must be used with a service name, not an IP address")
	}
	return portforward.ResolveServiceAndPort(ctx, host, s.Namespace, portStr, proto)
}

func (s *session) rerouteRemotePort(ap types.AddrPortProto, newPort uint16) {
	if newPort != ap.Port() {
		dlog.Debugf(s, "Rerouting %s via %d", ap, newPort)

		// Swap ports so that the port map reroutes requests for the new port to the original port.
		toPort := ap.Port()
		ap.AddrPort = netip.AddrPortFrom(ap.Addr(), newPort)
		s.l4PortMap.Store(ap, toPort)
	}
}

type clusterLookupResult struct {
	created time.Time
	rrs     dnsproxy.RRs
	rCode   int
	err     error
}

// clusterLookup sends a Lookup or LookupDNS request to the traffic-manager and returns the result.
func (s *session) clusterLookup(ctx context.Context, q *dns2.Question) (dnsproxy.RRs, int, error) {
	dlog.Debugf(ctx, "Lookup %s %q", dns2.TypeToString[q.Qtype], q.Name)
	s.dnsLookups++

	if s.lookupSequencer == nil || !(q.Qtype == dns2.TypeA || q.Qtype == dns2.TypeAAAA) {
		return s.complexClusterLookup(ctx, q)
	}

	// The lookupSequencer ensures that successive calls for the same name, whether they are A or AAAA, are not
	// performed concurrently. The traffic manager will return all known IPs for the name regardless of the type.
	result, _ := s.lookupSequencer.Compute(q.Name, func(oldValue clusterLookupResult, loaded bool) (newValue clusterLookupResult, op xsync.ComputeOp) {
		if loaded && time.Since(oldValue.created) < lookupSequencerTTL {
			return oldValue, xsync.CancelOp
		}
		rrs, rCode, err := s.simpleLookup(ctx, q)
		return clusterLookupResult{
			created: time.Now(),
			rrs:     rrs,
			rCode:   rCode,
			err:     err,
		}, xsync.UpdateOp
	})
	return result.rrs, result.rCode, result.err
}

func (s *session) simpleLookup(ctx context.Context, question *dns2.Question) (dnsproxy.RRs, int, error) {
	var lookupClient interface {
		Lookup(context.Context, *manager.LookupRequest, ...grpc.CallOption) (*manager.LookupResponse, error)
	}
	request := &manager.LookupRequest{Session: s.session, Name: question.Name}
	if ags := s.agentClients; ags != nil {
		lookupClient = ags.GetRandomAgent(ctx)
	}
	if lookupClient == nil {
		dlog.Debugf(ctx, "Using traffic-manager for lookup %q", question.Name)
		lookupClient = s.managerClient()
	} else {
		dlog.Debugf(ctx, "Using traffic-agent for lookup %q", question.Name)
	}
	resp, err := lookupClient.Lookup(ctx, request)
	if status.Code(err) == codes.Unimplemented {
		return s.complexClusterLookup(ctx, question)
	}
	if err != nil {
		s.dnsFailures++
		rCode := rcodeFromError(err)
		dlog.Errorf(ctx, "Lookup %q %s: %v", question.Name, dns2.RcodeToString[rCode], err)
		return nil, rCode, err
	}
	if len(resp.Ips) == 0 {
		return nil, dns2.RcodeNameError, nil
	}
	ips := make([]netip.Addr, len(resp.Ips))
	for i := range resp.Ips {
		_ = ips[i].UnmarshalBinary(resp.Ips[i])
	}
	if len(s.localTranslationSubnets) > 0 {
		for i, ip := range ips {
			ips[i], err = s.GetLocalIP(ip)
			if err != nil {
				return nil, dns2.RcodeServerFailure, err
			}
		}
	}
	ips4, ips6 := splitNameTypes(question.Name, ips)
	rrs := ensureBothFamilies(question.Name, ips4, ips6)
	rCode := dns2.RcodeSuccess
	return rrs, rCode, err
}

// rrHeader creates a common DNS RR header for INET class.
func rrHeader(name string, rrType uint16) dns2.RR_Header {
	return dns2.RR_Header{
		Name:   name,
		Rrtype: rrType,
		Class:  dns2.ClassINET,
	}
}

// splitNameTypes converts the binary-encoded IPs into A and AAAA resource records.
func splitNameTypes(name string, ips []netip.Addr) (dnsproxy.RRs, dnsproxy.RRs) {
	ips4 := make(dnsproxy.RRs, 0)
	ips6 := make(dnsproxy.RRs, 0)

	for _, addr := range ips {
		if addr.Is6() {
			ips6 = append(ips6, &dns2.AAAA{
				Hdr:  rrHeader(name, dns2.TypeAAAA),
				AAAA: addr.AsSlice(),
			})
		} else {
			ips4 = append(ips4, &dns2.A{
				Hdr: rrHeader(name, dns2.TypeA),
				A:   addr.AsSlice(),
			})
		}
	}
	return ips4, ips6
}

// ensureBothFamilies pads with an empty RR for the missing address family, preserving original behavior.
func ensureBothFamilies(name string, ips4, ips6 dnsproxy.RRs) dnsproxy.RRs {
	switch {
	case len(ips4) > 0 && len(ips6) == 0:
		ips6 = append(ips6, &dns2.AAAA{
			Hdr: rrHeader(name, dns2.TypeAAAA),
		})
	case len(ips6) > 0 && len(ips4) == 0:
		ips4 = append(ips4, &dns2.A{
			Hdr: rrHeader(name, dns2.TypeA),
		})
	}
	return append(ips4, ips6...)
}

// clusterLookup sends a LookupDNS request to the traffic-manager and returns the result.
func (s *session) complexClusterLookup(ctx context.Context, q *dns2.Question) (dnsproxy.RRs, int, error) {
	dnsResponse, err := s.managerClient().LookupDNS(ctx, &manager.DNSRequest{
		Session: s.session,
		Name:    q.Name,
		Type:    uint32(q.Qtype),
	})
	if err != nil {
		s.dnsFailures++
		rCode := rcodeFromError(err)
		dlog.Errorf(ctx, "Lookup %s %q %s: %T %v", dns2.TypeToString[q.Qtype], q.Name, dns2.RcodeToString[rCode], err, err)
		return nil, rCode, err
	}
	answer, rCode, err := dnsproxy.FromRPC(dnsResponse)
	if err != nil {
		s.dnsFailures++
		return nil, dns2.RcodeServerFailure, err
	}
	if len(s.localTranslationSubnets) > 0 {
		for _, rr := range answer {
			switch rr := rr.(type) {
			case *dns2.A:
				var addr netip.Addr
				addr, err = s.GetLocalIP(netip.AddrFrom4([4]byte(rr.A)))
				if err == nil {
					rr.A = addr.AsSlice()
				}
			case *dns2.AAAA:
				var addr netip.Addr
				addr, err = s.GetLocalIP(netip.AddrFrom16([16]byte(rr.AAAA)))
				if err == nil {
					rr.AAAA = addr.AsSlice()
				}
			}
			if err != nil {
				rCode = dns2.RcodeServerFailure
				break
			}
		}
	}
	return answer, rCode, err
}

// rcodeFromError maps lookup errors to appropriate DNS RCODEs.
func rcodeFromError(err error) int {
	switch {
	case errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, context.Canceled),
		status.Code(err) == codes.DeadlineExceeded,
		status.Code(err) == codes.Canceled:
		return dns2.RcodeNameError
	default:
		return dns2.RcodeServerFailure
	}
}

func (s *session) GetLocalIP(destinationIP netip.Addr) (netip.Addr, error) {
	var err error
	va, _ := s.localTranslationTable.LoadOrCompute(destinationIP, func() (netip.Addr, bool) {
		for _, sn := range s.localTranslationSubnets {
			if sn.Contains(destinationIP) {
				var nip netip.Addr
				nip, err = s.nextVirtualIP(sn.workload, destinationIP)
				return nip, err != nil
			}
		}
		return netip.Addr{}, true
	})
	if err == nil && va.IsValid() {
		destinationIP = va
	}
	return destinationIP, err
}

func (s *session) nextVirtualIP(workload string, destinationIP netip.Addr) (netip.Addr, error) {
	va, err := s.vipGenerator.Next()
	if err != nil {
		return va, err
	}
	s.virtualIPs.Store(va, agentVIP{workload: workload, destinationIP: destinationIP})
	return va, nil
}

func (s *session) getNetworkConfig() *rpc.NetworkConfig {
	mc := client.GetConfig(s)
	r := mc.Routing()
	if s.tunVif != nil {
		r.Subnets = s.tunVif.Router.GetRoutedSubnets()
	} else {
		r.Subnets = nil
	}
	if len(s.effectiveNeverProxy) > 0 {
		r.NeverProxy = make([]netip.Prefix, len(s.effectiveNeverProxy))
		copy(r.NeverProxy, s.effectiveNeverProxy)
	} else {
		r.NeverProxy = nil
	}
	if len(s.alsoProxySubnets) > 0 {
		r.AlsoProxy = make([]netip.Prefix, len(s.alsoProxySubnets))
		copy(r.AlsoProxy, s.alsoProxySubnets)
	} else {
		r.AlsoProxy = nil
	}
	if len(s.allowConflictingSubnets) > 0 {
		r.AllowConflicting = make([]netip.Prefix, len(s.allowConflictingSubnets))
		copy(r.AllowConflicting, s.allowConflictingSubnets)
	} else {
		r.AllowConflicting = nil
	}
	d := mc.DNS()
	if proc.RunningInContainer() && s.teleroute != nil {
		las := s.teleroute.DaemonAddresses()
		d.LocalAddresses = make([]netip.AddrPort, len(las))
		for i, addr := range s.teleroute.DaemonAddresses() {
			d.LocalAddresses[i] = netip.AddrPortFrom(addr, 53)
		}
	} else {
		if s.localDNS.IsValid() {
			d.LocalAddresses = []netip.AddrPort{s.localDNS}
		} else {
			d.LocalAddresses = nil
		}
	}
	d.VIFAddress = s.vifDNS

	var portMappings []string
	if psz := s.l4PortMap.Size(); psz > 0 {
		portMappings = make([]string, 0, psz)
		s.l4PortMap.Range(func(key types.AddrPortProto, origPort uint16) bool {
			portMappings = append(portMappings, fmt.Sprintf("%s:%d", types.AddrPortProto{AddrPort: netip.AddrPortFrom(key.Addr(), origPort), Proto: key.Proto}, key.Port()))
			return true
		})
	}
	js, _ := json.Marshal(mc)
	return &rpc.NetworkConfig{
		Session:          s.session,
		PortMappings:     portMappings,
		ClientConfig:     js,
		MappedNamespaces: s.MappedNamespaces,
		ManagerNamespace: k8s.GetManagerNamespace(s),
	}
}

func (s *session) configureDNS(vifDNS netip.AddrPort, localDNS netip.AddrPort) {
	s.vifDNS = vifDNS
	s.localDNS = localDNS
}

// shouldProxySubnet returns true unless the given subnet is covered by a subnet in the neverProxySubnets list.
func (s *session) shouldProxySubnet(name string, sn netip.Prefix) bool {
	if sn.Addr().IsLoopback() {
		dlog.Infof(s, "Will not proxy %s subnet %s, because it is loopback", name, sn)
		return false
	}
	for _, lt := range s.localTranslationSubnets {
		if subnet.Covers(lt.Prefix, sn) {
			dlog.Infof(s, "Will not proxy %s subnet %s, because it covered by --proxy-via %s=%s", name, sn, lt.Prefix, lt.workload)
			return false
		}
	}
	for _, nps := range s.neverProxySubnets {
		if subnet.Covers(nps, sn) {
			// Allow if there's an also-proxy that is smaller, contradicting the never-proxy
			for _, aps := range s.alsoProxySubnets {
				if subnet.Covers(nps, aps) && subnet.Covers(aps, sn) {
					dlog.Infof(s, "Will proxy %s subnet %s, because it is covered by also-proxy %s overriding never-proxy %s", name, sn, nps, aps)
					return true
				}
			}
			dlog.Infof(s, "Will not proxy %s subnet %s, because it is covered by never-proxy %s", name, sn, nps)
			return false
		}
	}
	for _, npx := range s.subnetViaWorkloads {
		if name == "service" && npx.Subnet == "service" || name == "pod" && npx.Subnet == "pods" {
			dlog.Infof(s, "Will not proxy %s subnet %s, because it is covered by --proxy-via %s=%s", name, sn, npx.Subnet, npx.Workload)
			return false
		}
	}
	return true
}

// networkReady returns a channel that is close when both the VIF and DNS are ready.
func (s *session) networkReady(ctx context.Context) <-chan error {
	rdy := make(chan error, 2)
	go func() {
		defer close(rdy)
		select {
		case <-ctx.Done():
			rdy <- ctx.Err()
		case err, ok := <-s.vifReady:
			if ok {
				rdy <- err
			} else {
				select {
				case <-ctx.Done():
				case <-s.dnsServer.Ready():
				}
			}
		}
	}()
	return rdy
}

func (s *session) watchClusterInfo(teleroutePort uint16) error {
	return watcher.WatchWithRetry(s, "WatchClusterInfo", client.GetConfig(s).Grpc().WatchRetryInterval,
		func(ctx context.Context) (grpc.ServerStreamingClient[manager.ClusterInfo], error) {
			return s.managerClient().WatchClusterInfo(ctx, s.session)
		},
		func(mgrInfo *manager.ClusterInfo) error {
			if err := s.readAdditionalRouting(mgrInfo); err != nil {
				return err
			}
			select {
			case <-s.vifReady:
				if err := s.onClusterInfo(mgrInfo); err != nil {
					if !errors.Is(err, context.Canceled) {
						dlog.Error(s, err)
					}
					return err
				}
			default:
				if err := s.onFirstClusterInfo(teleroutePort, mgrInfo); err != nil {
					if !errors.Is(err, context.Canceled) {
						dlog.Error(s, err)
					}
					return err
				}
			}
			return nil
		},
		// The user daemon will restore the session, and our managerClient will reconnect automatically
		// thanks to the built-in resilience in the port-forward logic, so there's no need for a repair
		// function here.
		nil,
	)
}

// createSubnetForDNSOnly will find a random IPv4 subnet that isn't currently routed and
// attach the DNS server to that subnet.
func (s *session) createSubnetForDNSOnly(mgrInfo *manager.ClusterInfo) {
	// Avoid alsoProxied and neverProxied
	avoid := make([]netip.Prefix, 0, len(s.alsoProxySubnets)+len(s.neverProxySubnets))
	avoid = append(avoid, s.alsoProxySubnets...)
	avoid = append(avoid, s.neverProxySubnets...)

	// Avoid the service subnets. They might be mapped with iptables (if running bare-metal) and
	// hence invisible when listing known routes.
	avoid = append(avoid, s.serviceSubnets...)

	// Avoid the pod subnets. They are probably visible as known routes, but we add them to
	// the avoid table to be sure.
	for _, ps := range mgrInfo.PodSubnets {
		avoid = append(avoid, iputil.RPCToPrefix(ps))
	}
	var err error
	if s.dnsServerSubnet, err = subnet.RandomIPv4Prefix(30, avoid); err != nil {
		dlog.Error(s, err)
	}
}

func (s *session) onFirstClusterInfo(teleroutePort uint16, mgrInfo *manager.ClusterInfo) (err error) {
	defer func() {
		if err != nil {
			s.vifReady <- err
		}
		close(s.vifReady)
	}()
	if s.podDaemon {
		return nil
	}
	if teleroutePort > 0 {
		// Always proxy pods and services when using the teleroute network, so that we can inject synthetic IP:s as needed. This means
		// never relying on Docker's default route to reach the cluster.
		s.proxyClusterPods = true
		s.proxyClusterSvcs = true
	} else {
		s.proxyClusterPods = !s.hasPodConnectivity(mgrInfo)
		s.proxyClusterSvcs = !s.hasSvcConnectivity(mgrInfo)
	}
	return s.onClusterInfo(mgrInfo)
}

func (s *session) defaultRouteDNS(mgrInfo *manager.ClusterInfo, dnsAddr netip.Addr, subnets []netip.Prefix) (netip.Addr, []netip.Prefix, error) {
	// We'll need to synthesize a subnet where we can attach the DNS service when the VIF isn't configured
	// from cluster subnets. But not on darwin systems, because there the DNS is controlled by /etc/resolver
	// entries appointing the DNS service directly via localhost:<port>.
	if s.vipGenerator != nil {
		if !s.dnsServerSubnet.IsValid() {
			s.createSubnetForDNSOnly(mgrInfo)
		}
		dlog.Infof(s, "Adding service subnet %s (for DNS only)", s.dnsServerSubnet)
		var wl string
		for _, snw := range s.subnetViaWorkloads {
			if sn, err := netip.ParsePrefix(snw.Subnet); err == nil && sn.Contains(dnsAddr) {
				wl = snw.Workload
				if wl == "local" {
					wl = ""
				}
			}
		}
		s.localTranslationSubnets = append(s.localTranslationSubnets, agentSubnet{
			Prefix:   s.dnsServerSubnet,
			workload: wl,
		})
		var err error
		dnsAddr, err = s.GetLocalIP(dnsAddr)
		if err != nil {
			return dnsAddr, subnets, err
		}
	} else {
		if !s.dnsServerSubnet.IsValid() {
			s.createSubnetForDNSOnly(mgrInfo)
		}
		dlog.Infof(s, "Adding service subnet %s (for DNS only)", s.dnsServerSubnet)
		subnets = append(subnets, s.dnsServerSubnet)
		dnsIP := s.dnsServerSubnet.Addr().AsSlice()
		dnsIP[len(dnsIP)-1] = 2
		dnsAddr, _ = netip.AddrFromSlice(dnsIP)
	}
	return dnsAddr, subnets, nil
}

func (s *session) onClusterInfo(mgrInfo *manager.ClusterInfo) (err error) {
	if s.podDaemon {
		return nil
	}
	dlog.Debugf(s, "WatchClusterInfo update")
	bwcompat.FixLegacyClusterInfo(mgrInfo)

	if mgrInfo.Routing == nil {
		mgrInfo.Routing = &manager.Routing{}
	}

	s.serviceSubnets = nil
	s.podSubnets = nil

	var subnets []netip.Prefix

	if s.proxyClusterSvcs {
		for _, sb := range mgrInfo.ServiceCidrs {
			var cidr netip.Prefix
			err = cidr.UnmarshalBinary(sb)
			if err != nil {
				return err
			}
			if s.shouldProxySubnet("service", cidr) {
				dlog.Infof(s, "Adding service subnet %s", cidr)
				subnets = append(subnets, cidr)
			}
			s.serviceSubnets = append(s.serviceSubnets, cidr)
		}
	}

	if s.proxyClusterPods {
		for _, sn := range mgrInfo.PodSubnets {
			cidr := iputil.RPCToPrefix(sn)
			if s.shouldProxySubnet("pod", cidr) {
				dlog.Infof(s, "Adding pod subnet %s", cidr)
				subnets = append(subnets, cidr)
			}
			s.podSubnets = append(s.podSubnets, cidr)
		}
	}

	if s.vipGenerator != nil {
		subnets = append(subnets, s.vipGenerator.Subnet())
		dlog.Debugf(s, "Adding VIP subnet %q to TUN-device", s.vipGenerator.Subnet().String())
		s.consolidateProxyViaWorkloads()
	}

	if !s.alsoProxyVia() {
		subnets = append(subnets, s.alsoProxySubnets...)
	}

	// We use the ManagerPodIp as the dnsIP. The reason for this is that no one should ever
	// talk to the traffic-manager directly using the TUN device, so it's safe to use its
	// IP to impersonate the DNS server. All traffic sent to that IP, will be routed to
	// the local DNS server.
	vifDNS, ok := netip.AddrFromSlice(mgrInfo.ManagerPodIp)
	if !ok {
		return fmt.Errorf("invalid traffic-manager pod ip address")
	}
	if s.vipGenerator != nil {
		vifDNS, err = s.GetLocalIP(vifDNS)
		if err != nil {
			return err
		}
	}
	dnsRouted := false
	if proc.RunningInContainer() {
		dnsRouted = true
	} else {
		for _, sn := range subnets {
			if sn.Contains(vifDNS) {
				dnsRouted = true
				break
			}
		}
	}
	if runtime.GOOS != "darwin" && !dnsRouted {
		vifDNS, subnets, err = s.defaultRouteDNS(mgrInfo, vifDNS, subnets)
		if err != nil {
			return err
		}
		dnsRouted = true
	}

	if dnsRouted {
		d := mgrInfo.Dns
		dnsAddress := netip.AddrPortFrom(vifDNS, 53)
		dlog.Infof(s, "Setting client DNS to %s", vifDNS)
		dlog.Infof(s, "Setting cluster domain to %q", d.ClusterDomain)
		s.dnsServer.SetClusterDNS(d, dnsAddress)
	}
	return s.reconcileSubnets(mgrInfo, subnets)
}

func (s *session) reconcileSubnets(mgrInfo *manager.ClusterInfo, subnets []netip.Prefix) error {
	if len(subnets) > 0 && s.tunVif == nil {
		var err error
		if s.tunVif, err = vif.NewTunnelingDevice(s, s.streamCreator()); err != nil {
			return fmt.Errorf("NewTunnelVIF: %w", err)
		}
	}

	proxy, neverProxy, neverProxyOverrides := computeNeverProxyOverrides(s, subnets, s.neverProxySubnets)
	s.effectiveNeverProxy = neverProxy
	if s.tunVif == nil {
		return nil
	}
	rt := s.tunVif.Router
	dlog.Debugf(s, "allowConflicting is set to %v", s.allowConflictingSubnets)
	rt.UpdateWhitelist(s.allowConflictingSubnets)

	err := rt.ValidateRoutes(s, proxy)
	if err != nil {
		if s.vipGenerator != nil {
			dlog.Debugf(s, "vipGenerator is defined so error %s does not result in any translations", err)
			return err
		}
		if !client.GetConfig(s).Routing().AutoResolveConflicts {
			dlog.Debugf(s, "autoResolveConflicts is false so %s isn't resolving itself", err)
			return err
		}
		// Check each subnet and add a translation for those that conflict.
		for _, pp := range proxy {
			if routeConflict := rt.ValidateRoutes(s, []netip.Prefix{pp}); routeConflict != nil {
				dlog.Infof(s, "Translating IPs in conflicting subnet %s to the virtual subnet", pp)
				s.subnetViaWorkloads = append(s.subnetViaWorkloads, &rpc.SubnetViaWorkload{
					Subnet:   pp.String(),
					Workload: "local",
				})
			}
		}
		if aErr := s.activateProxyViaWorkloads(); aErr != nil {
			dlog.Errorf(s, "activateProxyViaWorkloads: %v", aErr)
			return err
		}
		return s.onClusterInfo(mgrInfo)
	}

	dlog.Debugf(s, "UpdatingRoutes %s, %s, %s", proxy, s.effectiveNeverProxy, neverProxyOverrides)
	err = rt.UpdateRoutes(s, proxy, s.effectiveNeverProxy, neverProxyOverrides)
	if err != nil {
		return err
	}
	sns := rt.GetRoutedSubnets()
	select {
	case <-s.Done():
	case s.routesCh <- sns:
	default:
	}
	return nil
}

func computeNeverProxyOverrides(ctx context.Context, subnets, nvp []netip.Prefix) (proxy, neverProxy, neverProxyOverrides []netip.Prefix) {
	neverProxy = slices.DeleteFunc(slices.Clone(nvp), func(nps netip.Prefix) bool {
		for _, ds := range subnets {
			if ds.Overlaps(nps) {
				return false
			}
		}
		// This never-proxy is pointless because it's not a subnet that we are routing
		dlog.Infof(ctx, "Dropping never-proxy %q because it is not routed", nps)
		return true
	})

	proxy, neverProxyOverrides = subnet.Partition(subnets, func(i int, isn netip.Prefix) bool {
		for r, rsn := range subnets {
			if i == r {
				continue
			}
			if subnet.Covers(rsn, isn) && rsn != isn {
				for _, dsn := range neverProxy {
					if subnet.Covers(dsn, isn) {
						return false
					}
				}
			}
		}
		return true
	})
	return subnet.Unique(proxy), neverProxy, neverProxyOverrides
}

func validateSubnets(name string, ns []netip.Prefix, allowLoopback func() bool) ([]netip.Prefix, error) {
	ns = subnet.Unique(ns)
	rs := make([]netip.Prefix, 0, len(ns))
	for _, sn := range ns {
		if sn.Addr().IsLoopback() && !allowLoopback() {
			return nil, fmt.Errorf(`%s subnet %s is a loopback subnet. It is never proxied`, name, sn)
		}
		rs = append(rs, sn)
	}
	return subnet.Unique(rs), nil
}

// alsoProxyVia will return true when the connection was made using --subnet-via all=<workload> or --subnet-via also=<workload>.
func (s *session) alsoProxyVia() bool {
	for _, pvx := range s.subnetViaWorkloads {
		if pvx.Subnet == "also" { // no need to test for "all". It's normalized into ["also", "pods", "service"]
			return true
		}
	}
	return false
}

func (s *session) readAdditionalRouting(mgrInfo *manager.ClusterInfo) error {
	if r := mgrInfo.Routing; r != nil {
		sns, err := validateSubnets("also-proxy", iputil.RPCsToPrefixes(r.AlsoProxySubnets), s.alsoProxyVia)
		if err != nil {
			return err
		}
		s.alsoProxySubnets = subnet.Unique(append(s.alsoProxySubnets, sns...))
		dlog.Infof(s, "also-proxy subnets %v", s.alsoProxySubnets)

		sns, err = validateSubnets("never-proxy", iputil.RPCsToPrefixes(r.NeverProxySubnets), nope)
		if err != nil {
			return err
		}
		s.neverProxySubnets = subnet.Unique(append(s.neverProxySubnets, sns...))
		dlog.Infof(s, "never-proxy subnets %v", s.neverProxySubnets)

		sns, err = validateSubnets("allow-conflicting", iputil.RPCsToPrefixes(r.AllowConflictingSubnets), nope)
		if err != nil {
			return err
		}
		s.allowConflictingSubnets = subnet.Unique(append(s.allowConflictingSubnets, sns...))
		dlog.Infof(s, "allow-conflicting subnets %v", s.allowConflictingSubnets)
	}
	return nil
}

// hasSvcConnectivity checks the connectivity of the agent-injector server. It returns true if it can be connected, false otherwise.
func (s *session) hasSvcConnectivity(info *manager.ClusterInfo) bool {
	// The traffic-manager service is headless, which means we can't try a GRPC connection to its ClusterIP.
	// Instead, we try an HTTP health check on the agent-injector server, since that one does expose a ClusterIP.
	// This is less precise than if we could check for our own GRPC, since /healthz is a common enough health check path,
	// but hopefully, the server on the other end isn't configured to respond to the hostname "agent-injector" if it isn't the agent-injector.
	if info.InjectorSvcIp == nil {
		dlog.Debugf(s, "No injector service IP given; usually this is because the traffic-manager is older than the telepresence binary."+
			"Connectivity check for services set to pass.")
		return false
	}
	ct := client.GetConfig(s).Timeouts().Get(client.TimeoutConnectivityCheck)
	if ct == 0 {
		dlog.Info(s, "Connectivity check for services disabled")
		return false
	}
	ip := net.IP(info.InjectorSvcIp).String()
	port := info.InjectorSvcPort
	if port == 0 {
		port = 8443
	}
	tr := &http.Transport{
		// Skip checking the cert because its trust chain is loaded into a secret on the cluster; we'd fail to verify it
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	hcl := &http.Client{Transport: tr}
	tCtx, tCancel := context.WithTimeout(s, ct)
	defer tCancel()
	url := iputil.JoinHostPort(ip, uint16(port))
	url = fmt.Sprintf("https://%s/healthz", url)
	request, err := http.NewRequestWithContext(tCtx, http.MethodHead, url, nil)
	if err != nil {
		// As far as I can tell, this error means a) that the context was cancelled before the request could be allocated, or b) that the request is misconstructed, e.g. bad method.
		// Neither of those two should really happen here (unless you set the timeout to a few microseconds, maybe), but we can't really continue. May as well route the cluster.
		dlog.Errorf(s, "Unexpected: service conn check could not build request: %v. Will route services anyway.", err)
		return false
	}
	request.Header.Set("Host", info.InjectorSvcHost)
	dlog.Debugf(s, "Performing service connectivity check on %s with Host %s and timeout %s", url, info.InjectorSvcHost, ct)
	resp, err := hcl.Do(request)
	if err != nil {
		// This means either network errors (timeouts, failed to connect), or that the server doesn't speak HTTP.
		dlog.Debugf(s, "Will proxy services (%v)", err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		dlog.Warnf(s, "service IP %s is connectable, but did not respond as expected (status code %d)."+
			" Will proxy services, but this may interfere with your VPN routes.", info.InjectorSvcIp, resp.StatusCode)
		return false
	}
	dlog.Info(s, "Already connected to cluster, will not map service subnets.")
	return true
}

// hasPodConnectivity verifies connectivity to the traffic-manager in the cluster and returns true if connectivity is successful; false otherwise.
func (s *session) hasPodConnectivity(info *manager.ClusterInfo) bool {
	if info.ManagerPodIp == nil {
		return false
	}
	ct := client.GetConfig(s).Timeouts().Get(client.TimeoutConnectivityCheck)
	if ct == 0 {
		dlog.Info(s, "Connectivity check for pods disabled")
		return false
	}
	ip := net.IP(info.ManagerPodIp).String()
	port := info.ManagerPodPort
	if port == 0 {
		port = 8081 // Traffic managers before 2.8.0 didn't include the port because it was hardcoded at 8081
	}
	tCtx, tCancel := context.WithTimeout(s, ct)
	defer tCancel()
	dlog.Debugf(s, "Performing pod connectivity check on IP %s with timeout %s", ip, ct)
	conn, err := grpc.NewClient(
		net.JoinHostPort(ip, strconv.Itoa(int(port))),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		dlog.Debugf(s, "Will proxy pods. NewClient: %v", err)
		return false
	}
	state := conn.GetState()
	conn.Connect() // Initiate the connection
	defer conn.Close()

	// Wait for the connection to reach READY state or fail
	for {
		switch state {
		case connectivity.Ready:
			// Connection is established
			_, err = manager.NewManagerClient(conn).Version(tCtx, &empty.Empty{})
			if err != nil {
				if s.Err() != nil {
					return false // session cancelled
				}
				dlog.Warnf(s, "Manager IP %s is connectable but not a traffic-manager instance (%v)."+
					" Will proxy pods, but this may interfere with your VPN routes.", ip, err)
				return false
			}
			dlog.Info(s, "Will not proxy pods. Already connected to cluster.")
			return true
		case connectivity.TransientFailure, connectivity.Shutdown:
			dlog.Debugf(s, "Will proxy pods, connection failed: state=%v", state)
			return false
		default:
			if !conn.WaitForStateChange(tCtx, state) {
				// Normal. The context timed out before the connection reached READY state.
				dlog.Debug(s, "Will proxy pods")
				return false
			}
			state = conn.GetState()
		}
	}
}

func (s *session) run(initErrs chan<- error) {
	defer func() {
		dlog.Info(s, "-- session ended")
	}()
	g := dgroup.NewGroup(s, dgroup.GroupConfig{})
	if err := s.Start(g, 0); err != nil {
		defer close(initErrs)
		initErrs <- err
		return
	}
	close(initErrs)
	err := g.Wait()
	if err != nil {
		dlog.Errorf(s, "session ended with error: %v", err)
	}
}

func (s *session) Start(g *dgroup.Group, teleroutePort uint16) error {
	clusterCfg := client.GetConfig(s).Cluster()
	if clusterCfg.AgentPortForward {
		if k8s.CanPortForward(s, s.Namespace) {
			s.agentClients = agentpf.NewClients(s.Cluster, s.session)
			g.Go("agentPods", func(ctx context.Context) error {
				return s.agentClients.WatchAgentPods(s.managerClient())
			})
		} else {
			dlog.Infof(s, "Agent port-forwards are disabled. Client is not permitted to do port-forward to namespace %s", s.Namespace)
		}
	}
	if err := s.activateProxyViaWorkloads(); err != nil {
		return err
	}
	if s.podDaemon {
		return nil
	}

	cancelDNSLock := sync.Mutex{}
	cancelDNS := func() {}

	g.Go("network", func(ctx context.Context) error {
		defer func() {
			cancelDNSLock.Lock()
			cancelDNS()
			cancelDNSLock.Unlock()
		}()
		return s.watchClusterInfo(teleroutePort)
	})

	if s.agentClients == nil && len(s.subnetViaWorkloads) > 0 {
		return fmt.Errorf("--proxy-via can only be used when cluster.agentPortForward is enabled")
	}

	// At this point, we wait until the VIF is ready. It will be shortly after
	// the first ClusterInfo is received from the traffic-manager. A timeout
	// is needed so that we don't wait forever on a traffic-manager that has
	// been terminated for some reason.
	wc, cancel := client.GetConfig(s).Timeouts().TimeoutContext(s, client.TimeoutTrafficManagerConnect)
	defer cancel()
	select {
	case <-wc.Done():
		// Time out when waiting for the cluster info to arrive
		s.vifReady <- wc.Err()
		s.dnsServer.Stop()
		return wc.Err()
	case err := <-s.vifReady:
		if err != nil {
			s.dnsServer.Stop()
			return err
		}
	}

	// Start the router and the DNS service and wait for the context
	// to be done. Then shut things down in order. The following happens:
	// 1. The DNS worker terminates (it needs the TUN device to be alive while doing that)
	// 2. The TUN device is closed (by the stop method). This unblocks the routerWorker's pending read on the device.
	// 3. The routerWorker terminates.
	g.Go("dns", func(ctx context.Context) error {
		defer s.stop()
		cancelDNSLock.Lock()
		ctx, cancelDNS = context.WithCancel(ctx)
		cancelDNSLock.Unlock()
		var dev vif.Device
		if s.tunVif != nil {
			dev = s.tunVif.Device
		}
		return s.dnsServer.Worker(ctx, dev, s.configureDNS)
	})

	if s.tunVif != nil {
		g.Go("vif", s.tunVif.Run)
		err := s.waitForProxyViaWorkloads()
		if err != nil {
			return err
		}
		if teleroutePort > 0 {
			s.teleroute, err = teleroute.StartServer(g, s.tunVif, s.routesCh, teleroutePort)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *session) stop() {
	if !atomic.CompareAndSwapInt32(&s.closing, 0, 1) {
		// session already stopped (or is stopping)
		return
	}
	dlog.Debug(s, "Bringing down TUN-device")
	cc, cancel := context.WithTimeout(context.WithoutCancel(s), time.Second)
	go func() {
		s.handlers.CloseAll(cc)
		cancel()
	}()
	<-cc.Done()
	atomic.StoreInt32(&s.closing, 2)

	if s.managerConn != nil {
		dlog.Debug(s, "Closing port-forward to traffic-manager")
		// Avoid sporadic hang when the client connection is torn down.
		cc, cancel = context.WithTimeout(context.WithoutCancel(s), time.Second)
		go func() {
			_ = s.managerConn.Close()
			cancel()
		}()
		<-cc.Done()
	}

	if s.tunVif != nil {
		cc, cancel = context.WithTimeout(context.WithoutCancel(s), time.Second)
		defer cancel()
		if err := s.tunVif.Close(cc); err != nil {
			dlog.Errorf(s, "unable to close %s: %v", s.tunVif.Device.Name(), err)
		}
	}
}

func (s *session) activateProxyViaWorkloads() error {
	sl := len(s.subnetViaWorkloads)
	if sl == 0 {
		return nil
	}
	vipSubnet := client.GetConfig(s).Routing().VirtualSubnet
	dlog.Debugf(s, "ProxyVIA using subnet %s", vipSubnet)

	s.vipGenerator = vip.NewGenerator(vipSubnet)
	s.localTranslationSubnets = make([]agentSubnet, sl)
	for _, wlName := range s.consolidateProxyViaWorkloads() {
		if s.agentClients == nil {
			return errcat.User.Newf("Agent port-forwards are disabled. Client is not permitted to do proxy-via %s", wlName)
		}
		dlog.Debugf(s, "Ensuring proxy-via agent in %s", wlName)
		_, err := s.managerClient().EnsureAgent(s, &manager.EnsureAgentRequest{
			Session: s.session,
			Name:    wlName,
		})
		if err != nil {
			if st, ok := status.FromError(err); ok {
				if st.Code() == codes.FailedPrecondition {
					return errcat.User.New(st.Message())
				}
			}
			return err
		}
	}
	return nil
}

func (s *session) consolidateProxyViaWorkloads() []string {
	desiredVips := make(map[string][]netip.Prefix)
	snCount := 0
	for _, pvx := range s.subnetViaWorkloads {
		switch pvx.Subnet {
		case "also":
			desiredVips[pvx.Workload] = append(desiredVips[pvx.Workload], s.alsoProxySubnets...)
			snCount += len(s.alsoProxySubnets)
		case "pods":
			desiredVips[pvx.Workload] = append(desiredVips[pvx.Workload], s.podSubnets...)
			snCount += len(s.podSubnets)
		case "service":
			desiredVips[pvx.Workload] = append(desiredVips[pvx.Workload], s.serviceSubnets...)
			snCount += len(s.serviceSubnets)
		default:
			sn, err := netip.ParsePrefix(pvx.Subnet)
			if err != nil {
				dlog.Warnf(s, "unable to parse proxy-via subnet %s", pvx.Subnet)
			} else {
				desiredVips[pvx.Workload] = append(desiredVips[pvx.Workload], sn)
				snCount++
			}
		}
	}

	wlNames := make([]string, 0, len(desiredVips))
	lcs := make([]agentSubnet, 0, snCount)
	for wlName, sns := range desiredVips {
		if wlName == "local" {
			wlName = ""
		} else {
			wlNames = append(wlNames, wlName)
		}
		for _, sn := range sns {
			lcs = append(lcs, agentSubnet{Prefix: sn, workload: wlName})
		}
	}
	s.localTranslationSubnets = lcs
	dlog.Debugf(s, "Local translation subnets: %v", s.localTranslationSubnets)
	return wlNames
}

func (s *session) waitForProxyViaWorkloads() error {
	wc := len(s.subnetViaWorkloads)
	if wc == 0 {
		return nil
	}
	to := client.GetConfig(s).Timeouts().Get(client.TimeoutIntercept)
	waitCh := make(chan error)

	// Need unique workload names
	ws := make([]string, 0, len(s.subnetViaWorkloads))
	for _, svw := range s.subnetViaWorkloads {
		if svw.Workload != "local" {
			ws = slice.AppendUnique(ws, svw.Workload)
		}
	}
	for _, wl := range ws {
		s.agentClients.SetProxyVia(wl)
		dlog.Debugf(s, "Waiting for proxy-via agent in %s", wl)
		go func(wl string) {
			waitCh <- s.agentClients.WaitForWorkload(to, wl)
		}(wl)
	}
	for _, wl := range ws {
		select {
		case <-s.Done():
			return nil
		case err := <-waitCh:
			if err != nil {
				return fmt.Errorf("proxy-via agent in %s failed: %w", wl, err)
			}
			dlog.Debugf(s, "Wait succeeded for proxy-via agent in %s", wl)
		}
	}
	return nil
}

func (s *session) SetTopLevelDomains(topLevelDomains []string) {
	s.dnsServer.SetTopLevelDomainsAndSearchPath(s, topLevelDomains, s.Namespace)
}

func (s *session) SetExcludes(excludes []string) {
	s.dnsServer.SetExcludes(excludes)
}

func (s *session) SetMappings(mappings []*rpc.DNSMapping) {
	s.dnsServer.SetMappings(mappings)
}

func (s *session) translateEnvIPs(environment *rpc.Environment) *rpc.Environment {
	vip.TranslateEnvironmentIPs(s, environment.Env, s)
	return environment
}

func (s *session) lookupIP(rq *rpc.LookupIPRequest) (*rpc.LookupIPResponse, error) {
	ip, err := dns.LookupIP(s, s.localDNS, rq.Name)
	if err != nil {
		return nil, err
	}
	rsp := new(rpc.LookupIPResponse)
	rsp.Ip, _ = ip.MarshalBinary()
	return rsp, nil
}

func (s *session) MapsIPv4() bool {
	for _, p := range s.localTranslationSubnets {
		if p.Addr().Is4() {
			return true
		}
	}
	return false
}

func (s *session) MapsIPv6() bool {
	for _, p := range s.localTranslationSubnets {
		if p.Addr().Is6() {
			return true
		}
	}
	return false
}

func (s *session) waitForAgentIP(ctx context.Context, request *rpc.WaitForAgentIPRequest) (*rpc.WaitForAgentIPResponse, error) {
	if s.agentClients == nil {
		return nil, status.Error(codes.Unavailable, "")
	}
	ip, ok := netip.AddrFromSlice(request.Ip)
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "")
	}
	err := s.agentClients.WaitForIP(ctx, request.Timeout.AsDuration(), ip)
	if err != nil {
		return nil, grpcErrors.FromError(err, codes.Internal, err.Error())
	}
	ip, err = s.GetLocalIP(ip)
	if err != nil {
		return nil, err
	}
	return &rpc.WaitForAgentIPResponse{LocalIp: ip.AsSlice()}, nil
}

func (s *session) ManagerVersion() semver.Version {
	return s.managerVersion
}

func (s *session) DialTCP(ctx context.Context, addr netip.AddrPort) (conn net.Conn, err error) {
	var d tunnel.Dialer
	if s.tunVif != nil && s.tunVif.Router.Routes(addr.Addr()) {
		d = s.tunVif
	} else {
		d = tunnel.DefaultDialer{}
	}
	return d.DialTCP(ctx, addr)
}

func (s *session) DialUDP(ctx context.Context, localAddr netip.AddrPort, remoteAddr netip.AddrPort) (conn net.Conn, err error) {
	var d tunnel.Dialer
	if s.tunVif != nil && s.tunVif.Router.Routes(remoteAddr.Addr()) {
		d = s.tunVif
	} else {
		d = tunnel.DefaultDialer{}
	}
	return d.DialUDP(ctx, localAddr, remoteAddr)
}
