package rootd

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blang/semver"
	dns2 "github.com/miekg/dns"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"
	"gopkg.in/yaml.v3"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/rootd/dns"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/client/socket"
	"github.com/telepresenceio/telepresence/v2/pkg/dnsproxy"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
	"github.com/telepresenceio/telepresence/v2/pkg/tracing"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/vif"
)

// Session resolves DNS names and routes outbound traffic that is centered around a TUN device. The router is
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
// A zero Session is invalid; you must use newSession.
type Session struct {
	scout *scout.Reporter

	tunVif *vif.TunnelingDevice

	// clientConn is the connection that uses the connector's socket
	clientConn *grpc.ClientConn

	// managerClient provides the gRPC tunnel to the traffic-manager
	managerClient connector.ManagerProxyClient

	// managerVersion is the version of the connected traffic-manager
	managerVersion semver.Version

	// connPool contains handlers that represent active connections. Those handlers
	// are obtained using a connpool.ConnID.
	handlers *tunnel.Pool

	// The local dns server
	dnsServer *dns.Server

	// remoteDnsIP is the IP of the DNS server attached to the TUN device. This is currently only
	// used in conjunction with systemd-resolved. The current macOS and the overriding solution
	// will dispatch directly to the local DNS Service without going through the TUN device but
	// that may change later if we decide to dispatch to the DNS-server in the cluster.
	remoteDnsIP net.IP

	// dnsLocalAddr is address of the local DNS Service.
	dnsLocalAddr *net.UDPAddr

	// Cluster subnets reported by the traffic-manager
	clusterSubnets []*net.IPNet

	// Subnets configured by the user
	alsoProxySubnets []*net.IPNet

	// Subnets configured by the user to never be proxied
	neverProxySubnets []*net.IPNet

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
	dnsServerSubnet *net.IPNet

	// vifReady is closed when the virtual network interface has been configured.
	vifReady chan error

	// config is the session config given by the traffic manager
	config client.Config

	// done is closed when the session ends
	done chan struct{}
}

type NewSessionFunc func(context.Context, *scout.Reporter, *rpc.OutboundInfo) (*Session, error)

type newSessionKey struct{}

func WithNewSessionFunc(ctx context.Context, f NewSessionFunc) context.Context {
	return context.WithValue(ctx, newSessionKey{}, f)
}

func GetNewSessionFunc(ctx context.Context) NewSessionFunc {
	if f, ok := ctx.Value(newSessionKey{}).(NewSessionFunc); ok {
		return f
	}
	panic("No User daemon Session creator has been registered")
}

// connectToManager connects to the traffic-manager and asserts that its version is compatible.
func connectToUserDaemon(c context.Context) (*grpc.ClientConn, connector.ManagerProxyClient, semver.Version, error) {
	// First check. Establish connection
	clientConfig := client.GetConfig(c)
	tos := &clientConfig.Timeouts
	tc, cancel := tos.TimeoutContext(c, client.TimeoutTrafficManagerAPI)
	defer cancel()

	var conn *grpc.ClientConn
	conn, err := socket.Dial(tc, socket.UserDaemonPath(c),
		grpc.WithUnaryInterceptor(otelgrpc.UnaryClientInterceptor()),
		grpc.WithStreamInterceptor(otelgrpc.StreamClientInterceptor()),
	)
	var mgrVer semver.Version
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// The connector called us, and then it died which means we will die too. This is
			// a race, but it's not an error.
			return nil, nil, mgrVer, nil
		}
		return nil, nil, mgrVer, client.CheckTimeout(tc, err)
	}

	mc := connector.NewManagerProxyClient(conn)
	ver, err := mc.Version(c, &empty.Empty{})
	if err != nil {
		conn.Close()
		return nil, nil, mgrVer, fmt.Errorf("failed to retrieve manager version: %w", err)
	}

	verStr := strings.TrimPrefix(ver.Version, "v")
	dlog.Infof(c, "Connected to Manager %s", verStr)
	mgrVer, err = semver.Parse(verStr)
	if err != nil {
		conn.Close()
		return nil, nil, mgrVer, fmt.Errorf("failed to parse manager version %q: %w", verStr, err)
	}

	if mgrVer.LE(semver.MustParse("2.4.4")) {
		conn.Close()
		return nil, nil, mgrVer, errcat.User.Newf("unsupported traffic-manager version %s. Minimum supported version is 2.4.5", mgrVer)
	}
	return conn, mc, mgrVer, nil
}

// NewSession returns a new properly initialized session object.
func NewSession(c context.Context, scout *scout.Reporter, mi *rpc.OutboundInfo) (*Session, error) {
	dlog.Info(c, "-- Starting new session")

	conn, mc, ver, err := connectToUserDaemon(c)
	if mc == nil || err != nil {
		return nil, err
	}
	s := newSession(c, scout, mi, mc, ver)
	s.clientConn = conn
	return s, nil
}

func newSession(c context.Context, scout *scout.Reporter, mi *rpc.OutboundInfo, mc connector.ManagerProxyClient, ver semver.Version) *Session {
	cfg := client.GetDefaultConfig()
	cliCfg, err := mc.GetClientConfig(c, &empty.Empty{})
	if err != nil {
		dlog.Warnf(c, "Failed to get remote config from traffic manager: %v", err)
	} else {
		err := yaml.Unmarshal(cliCfg.ConfigYaml, &cfg)
		if err != nil {
			dlog.Warnf(c, "Failed to deserialize remote config: %v", err)
		}
	}

	as := iputil.ConvertSubnets(mi.AlsoProxySubnets)
	ns := iputil.ConvertSubnets(mi.NeverProxySubnets)
	s := &Session{
		scout:             scout,
		handlers:          tunnel.NewPool(),
		rndSource:         rand.NewSource(time.Now().UnixNano()),
		session:           mi.Session,
		managerClient:     mc,
		managerVersion:    ver,
		alsoProxySubnets:  as,
		neverProxySubnets: ns,
		proxyClusterPods:  true,
		proxyClusterSvcs:  true,
		vifReady:          make(chan error, 2),
		config:            cfg,
		done:              make(chan struct{}),
	}

	if dnsproxy.ManagerCanDoDNSQueryTypes(ver) {
		s.dnsServer = dns.NewServer(mi.Dns, s.clusterLookup, false)
	} else {
		s.dnsServer = dns.NewServer(mi.Dns, s.legacyClusterLookup, true)
	}
	s.SetSearchPath(c, nil, nil)
	dlog.Infof(c, "also-proxy subnets %v", as)
	dlog.Infof(c, "never-proxy subnets %v", ns)
	return s
}

// clusterLookup sends a LookupDNS request to the traffic-manager and returns the result.
func (s *Session) clusterLookup(ctx context.Context, q *dns2.Question) (dnsproxy.RRs, int, error) {
	dlog.Debugf(ctx, "Lookup %s %q", dns2.TypeToString[q.Qtype], q.Name)
	s.dnsLookups++

	r, err := s.managerClient.LookupDNS(ctx, &manager.DNSRequest{
		Session: s.session,
		Name:    q.Name,
		Type:    uint32(q.Qtype),
	})
	if err != nil {
		s.dnsFailures++
		return nil, dns2.RcodeServerFailure, err
	}
	return dnsproxy.FromRPC(r)
}

// clusterLookup sends a LookupHost request to the traffic-manager and returns the result.
func (s *Session) legacyClusterLookup(ctx context.Context, q *dns2.Question) (rrs dnsproxy.RRs, rCode int, err error) {
	qType := q.Qtype
	if !(qType == dns2.TypeA || qType == dns2.TypeAAAA) {
		return nil, dns2.RcodeNotImplemented, nil
	}
	dlog.Debugf(ctx, "Lookup %s %q", dns2.TypeToString[q.Qtype], q.Name)
	s.dnsLookups++

	var r *manager.LookupHostResponse //nolint:staticcheck // retained for backward compatibility
	if r, err = s.managerClient.LookupHost(ctx, &manager.LookupHostRequest{Session: s.session, Name: q.Name[:len(q.Name)-1]}); err != nil {
		s.dnsFailures++
		return nil, dns2.RcodeServerFailure, err
	}
	ips := iputil.IPsFromBytesSlice(r.Ips)
	if len(ips) == 0 {
		return nil, dns2.RcodeNameError, nil
	}
	rrHeader := func() dns2.RR_Header {
		return dns2.RR_Header{Name: q.Name, Rrtype: qType, Class: dns2.ClassINET, Ttl: 4}
	}
	for _, ip := range ips {
		if ip4 := ip.To4(); ip4 != nil {
			rrs = append(rrs, &dns2.A{
				Hdr: rrHeader(),
				A:   ip4,
			})
		}
	}
	return rrs, dns2.RcodeSuccess, nil
}

func (s *Session) getNetworkConfig() *rpc.NetworkConfig {
	info := rpc.OutboundInfo{
		Session: s.session,
		Dns:     s.dnsServer.GetConfig(),
	}
	nc := &rpc.NetworkConfig{
		OutboundInfo: &info,
	}
	if s.dnsLocalAddr != nil {
		info.Dns.RemoteIp = s.dnsLocalAddr.IP
	}
	if len(s.alsoProxySubnets) > 0 {
		info.AlsoProxySubnets = make([]*manager.IPNet, len(s.alsoProxySubnets))
		for i, ap := range s.alsoProxySubnets {
			info.AlsoProxySubnets[i] = iputil.IPNetToRPC(ap)
		}
	}

	if len(s.neverProxySubnets) > 0 {
		info.NeverProxySubnets = make([]*manager.IPNet, len(s.neverProxySubnets))
		for i, np := range s.neverProxySubnets {
			info.NeverProxySubnets[i] = iputil.IPNetToRPC(np)
		}
	}
	if s.tunVif != nil {
		curSubnets := s.tunVif.Router.GetRoutedSubnets()
		nc.Subnets = make([]*manager.IPNet, len(curSubnets))
		for i, sn := range curSubnets {
			nc.Subnets[i] = iputil.IPNetToRPC(sn)
		}
	}
	return nc
}

func (s *Session) configureDNS(dnsIP net.IP, dnsLocalAddr *net.UDPAddr) {
	s.remoteDnsIP = dnsIP
	s.dnsLocalAddr = dnsLocalAddr
}

func (s *Session) refreshSubnets(ctx context.Context) (err error) {
	if s.tunVif == nil {
		dlog.Debug(ctx, "no tunnel, not refreshing subnets")
		return nil
	}
	// Create a unique slice of all desired subnets.
	ctx, span := otel.GetTracerProvider().Tracer("").Start(ctx, "refreshSubnets")
	defer tracing.EndAndRecord(span, err)
	desired := make([]*net.IPNet, len(s.clusterSubnets)+len(s.alsoProxySubnets))
	copy(desired, s.clusterSubnets)
	copy(desired[len(s.clusterSubnets):], s.alsoProxySubnets)
	desired = subnet.Unique(desired)

	return s.tunVif.Router.UpdateRoutes(ctx, desired, s.neverProxySubnets)
}

// networkReady returns a channel that is close when both the VIF and DNS are ready.
func (s *Session) networkReady(ctx context.Context) <-chan error {
	rdy := make(chan error, 2)
	go func() {
		defer close(rdy)
		select {
		case <-ctx.Done():
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

func (s *Session) watchClusterInfo(ctx context.Context) error {
	backoff := 100 * time.Millisecond

	for ctx.Err() == nil {
		infoStream, err := s.managerClient.WatchClusterInfo(ctx, s.session)
		if err != nil {
			err = fmt.Errorf("error when calling WatchClusterInfo: %w", err)
			dlog.Warn(ctx, err)
			return err
		}

		for ctx.Err() == nil {
			mgrInfo, err := infoStream.Recv()
			if err != nil {
				if gErr, ok := status.FromError(err); ok {
					switch gErr.Code() {
					case codes.Canceled:
						// The connector, which is routing this connection, cancelled it, which means that the client
						// session is dead.
						return nil
					case codes.Unavailable:
						// Abrupt shutdown. This is nothing that the session should survive
						dlog.Errorf(ctx, "WatchClusterInfo recv: Unavailable: %v", gErr.Message())
					}
				} else {
					dlog.Errorf(ctx, "WatchClusterInfo recv: %v", err)
				}
				break
			}
			ctx, span := otel.GetTracerProvider().Tracer("").Start(ctx, "ClusterInfoUpdate")
			select {
			case <-s.vifReady:
				if err := s.onClusterInfo(ctx, mgrInfo, span); err != nil {
					if !errors.Is(err, context.Canceled) {
						dlog.Error(ctx, err)
					}
				}
			default:
				if err = s.onFirstClusterInfo(ctx, mgrInfo, span); err != nil {
					if !errors.Is(err, context.Canceled) {
						dlog.Error(ctx, err)
					}
					return err
				}
			}
			span.End()
		}
		dtime.SleepWithContext(ctx, backoff)
		backoff *= 2
		if backoff > 15*time.Second {
			backoff = 15 * time.Second
		}
	}
	return nil
}

// createSubnetForDNSOnly will find a random IPv4 subnet that isn't currently routed and
// attach the DNS server to that subnet.
func (s *Session) createSubnetForDNSOnly(ctx context.Context, mgrInfo *manager.ClusterInfo) {
	// Avoid alsoProxied and neverProxied
	avoid := make([]*net.IPNet, 0, len(s.alsoProxySubnets)+len(s.neverProxySubnets))
	avoid = append(avoid, s.alsoProxySubnets...)
	avoid = append(avoid, s.neverProxySubnets...)

	// Avoid the service subnet. It might be mapped with iptables (if running bare-metal) and
	// hence invisible when listing known routes.
	if mgrInfo.ServiceSubnet != nil {
		avoid = append(avoid, iputil.IPNetFromRPC(mgrInfo.ServiceSubnet))
	}

	// Avoid the pod subnets. They are probably visible as known routes, but we add them to
	// the avoid table to be sure.
	for _, ps := range mgrInfo.PodSubnets {
		avoid = append(avoid, iputil.IPNetFromRPC(ps))
	}
	var err error
	if s.dnsServerSubnet, err = subnet.RandomIPv4Subnet(net.CIDRMask(30, 32), avoid); err != nil {
		dlog.Error(ctx, err)
	}
}

func (s *Session) onFirstClusterInfo(ctx context.Context, mgrInfo *manager.ClusterInfo, span trace.Span) (err error) {
	defer func() {
		if err != nil {
			s.vifReady <- err
		}
		close(s.vifReady)
	}()
	s.proxyClusterPods = s.checkPodConnectivity(ctx, mgrInfo)
	s.proxyClusterSvcs = s.checkSvcConnectivity(ctx, mgrInfo)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	s.readAdditionalRouting(ctx, mgrInfo)
	willProxy := s.proxyClusterSvcs || s.proxyClusterPods || len(s.alsoProxySubnets) > 0

	// We'll need to synthesize a subnet where we can attach the DNS service when the VIF isn't configured
	// from cluster subnets. But not on darwin systems, because there the DNS is controlled by /etc/resolver
	// entries appointing the DNS service directly via localhost:<port>.
	if !willProxy && runtime.GOOS != "darwin" {
		s.createSubnetForDNSOnly(ctx, mgrInfo)
	}

	// Do we need a VIF? A darwin system with full cluster access doesn't.
	if willProxy || s.dnsServerSubnet != nil {
		if s.tunVif, err = vif.NewTunnelingDevice(ctx, s.streamCreator()); err != nil {
			return fmt.Errorf("NewTunnelVIF: %v", err)
		}
		// TODO: Set a 0.0.0.0/0 whitelist until we wire in user-configured whitelist.
		s.tunVif.Router.UpdateWhitelist([]*net.IPNet{{IP: net.IPv4zero, Mask: net.IPv4Mask(0, 0, 0, 0)}})
	}
	return s.onClusterInfo(ctx, mgrInfo, span)
}

func (s *Session) onClusterInfo(ctx context.Context, mgrInfo *manager.ClusterInfo, span trace.Span) error {
	dlog.Debugf(ctx, "WatchClusterInfo update")
	dns := mgrInfo.Dns
	if dns == nil {
		// Older traffic-manager. Use deprecated mgrInfo fields for DNS
		dns = &manager.DNS{
			ClusterDomain: mgrInfo.ClusterDomain,
		}
	}

	s.readAdditionalRouting(ctx, mgrInfo)

	var subnets []*net.IPNet
	if s.proxyClusterSvcs {
		if mgrInfo.ServiceSubnet != nil {
			cidr := iputil.IPNetFromRPC(mgrInfo.ServiceSubnet)
			dlog.Infof(ctx, "Adding Service subnet %s", cidr)
			subnets = append(subnets, cidr)
		}
	}

	if s.proxyClusterPods {
		for _, sn := range mgrInfo.PodSubnets {
			cidr := iputil.IPNetFromRPC(sn)
			dlog.Infof(ctx, "Adding pod subnet %s", cidr)
			subnets = append(subnets, cidr)
		}
	}

	var dnsIP net.IP
	if s.dnsServerSubnet != nil {
		// None of the cluster's subnets are routed, so add this subnet instead and unconditionally
		// use the first available IP for our DNS server.
		dlog.Infof(ctx, "Adding Service subnet %s (for DNS only)", s.dnsServerSubnet)
		subnets = append(subnets, s.dnsServerSubnet)
		dnsIP = make(net.IP, len(s.dnsServerSubnet.IP))
		copy(dnsIP, s.dnsServerSubnet.IP)
		dnsIP[len(dnsIP)-1] = 2
	} else {
		// We use the ManagerPodIp as the dnsIP. The reason for this is that no one should ever
		// talk to the traffic-manager directly using the TUN device, so it's safe to use its
		// IP to impersonate the DNS server. All traffic sent to that IP, will be routed to
		// the local DNS server.
		dnsIP = mgrInfo.ManagerPodIp
	}

	dlog.Infof(ctx, "Setting cluster DNS to %s", dnsIP)
	dlog.Infof(ctx, "Setting cluster domain to %q", dns.ClusterDomain)
	s.dnsServer.SetClusterDNS(dns, dnsIP)

	s.clusterSubnets = subnet.Unique(subnets)
	if err := s.refreshSubnets(ctx); err != nil {
		return err
	}

	span.SetAttributes(
		attribute.Bool("tel2.proxy-svcs", s.proxyClusterSvcs),
		attribute.Bool("tel2.proxy-pods", s.proxyClusterPods),
		attribute.Stringer("tel2.cluster-dns", net.IP(dns.KubeIp)),
		attribute.String("tel2.cluster-domain", dns.ClusterDomain),
	)
	return nil
}

func (s *Session) readAdditionalRouting(ctx context.Context, mgrInfo *manager.ClusterInfo) {
	if r := mgrInfo.Routing; r != nil {
		as := subnet.Unique(append(s.alsoProxySubnets, iputil.ConvertSubnets(r.AlsoProxySubnets)...))
		dlog.Infof(ctx, "also-proxy subnets %v", as)
		s.alsoProxySubnets = as

		ns := subnet.Unique(append(s.neverProxySubnets, iputil.ConvertSubnets(r.NeverProxySubnets)...))
		dlog.Infof(ctx, "never-proxy subnets %v", ns)
		s.neverProxySubnets = ns
	}
}

func (s *Session) checkSvcConnectivity(ctx context.Context, info *manager.ClusterInfo) bool {
	// The traffic-manager service is headless, which means we can't try a GRPC connection to its ClusterIP.
	// Instead, we try an HTTP health check on the agent-injector server, since that one does expose a ClusterIP.
	// This is less precise than if we could check for our own GRPC, since /healthz is a common enough health check path,
	// but hopefully the server on the other end isn't configured to respond to the hostname "agent-injector" if it isn't the agent-injector.
	if info.InjectorSvcIp == nil {
		dlog.Debugf(ctx, "No injector service IP given; usually this is because the traffic-manager is older than the telepresence binary."+
			"Connectivity check for services set to pass.")
		return true
	}
	ct := client.GetConfig(ctx).Timeouts.Get(client.TimeoutConnectivityCheck)
	if ct == 0 {
		dlog.Info(ctx, "Connectivity check for services disabled")
		return true
	}
	ip := net.IP(info.InjectorSvcIp).String()
	port := info.InjectorSvcPort
	if port == 0 {
		port = 443
	}
	tr := &http.Transport{
		// Skip checking the cert because its trust chain is loaded into a secret on the cluster; we'd fail to verify it
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: tr}
	tCtx, tCancel := context.WithTimeout(ctx, ct)
	defer tCancel()
	url := net.JoinHostPort(ip, strconv.Itoa(int(port)))
	url = fmt.Sprintf("https://%s/healthz", url)
	request, err := http.NewRequestWithContext(tCtx, http.MethodGet, url, nil)
	if err != nil {
		// As far as I can tell, this error means a) that the context was cancelled before the request could be allocated, or b) that the request is misconstructed, e.g. bad method.
		// Neither of those two should really happen here (unless you set the timeout to a few microseconds, maybe), but we can't really continue. May as well route the cluster.
		dlog.Errorf(ctx, "Unexpected: service conn check could not build request: %v. Will route services anyway.", err)
		return true
	}
	request.Header.Set("Host", info.InjectorSvcHost)
	dlog.Debugf(ctx, "Performing service connectivity check on %s with Host %s and timeout %s", url, info.InjectorSvcHost, ct)
	resp, err := client.Do(request)
	if err != nil {
		// This means either network errors (timeouts, failed to connect), or that the server doesn't speak HTTP.
		dlog.Debugf(ctx, "Will proxy services (%v)", err)
		return true
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		dlog.Warnf(ctx, "Service IP %s is connectable, but did not respond as expected (status code %d)."+
			" Will proxy services, but this may interfere with your VPN routes.", info.InjectorSvcIp, resp.StatusCode)
		return true
	}
	dlog.Info(ctx, "Already connected to cluster, will not map service subnets.")
	return false
}

func (s *Session) checkPodConnectivity(ctx context.Context, info *manager.ClusterInfo) bool {
	if info.ManagerPodIp == nil {
		return true
	}
	ct := client.GetConfig(ctx).Timeouts.Get(client.TimeoutConnectivityCheck)
	if ct == 0 {
		dlog.Info(ctx, "Connectivity check for pods disabled")
		return true
	}
	ip := net.IP(info.ManagerPodIp).String()
	port := info.ManagerPodPort
	if port == 0 {
		port = 8081 // Traffic managers before 2.8.0 didn't include the port because it was hardcoded at 8081
	}
	tCtx, tCancel := context.WithTimeout(ctx, ct)
	defer tCancel()
	dlog.Debugf(ctx, "Performing pod connectivity check on IP %s with timeout %s", ip, ct)
	conn, err := grpc.DialContext(tCtx, fmt.Sprintf("%s:%d", ip, port), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		dlog.Debugf(ctx, "Will proxy pods (%v)", err)
		return true
	}
	defer conn.Close()
	mClient := manager.NewManagerClient(conn)
	if _, err := mClient.Version(tCtx, &empty.Empty{}); err != nil {
		dlog.Warnf(ctx, "Manager IP %s is connectable but not a traffic-manager instance (%v)."+
			" Will proxy pods, but this may interfere with your VPN routes.", ip, err)
		return true
	}
	dlog.Info(ctx, "Already connected to cluster, will not map pod subnets.")
	return false
}

func (s *Session) run(c context.Context, initErrs chan error) error {
	defer func() {
		dlog.Info(c, "-- Session ended")
		if s.clientConn != nil {
			_ = s.clientConn.Close()
		}
		close(s.done)
	}()

	c, cancelGroup := context.WithCancel(c)
	defer cancelGroup()

	g := dgroup.NewGroup(c, dgroup.GroupConfig{})
	if err := s.Start(c, g); err != nil {
		defer close(initErrs)
		initErrs <- err
		return err
	}
	close(initErrs)
	return g.Wait()
}

func (s *Session) Start(c context.Context, g *dgroup.Group) error {
	cancelDNSLock := sync.Mutex{}
	cancelDNS := func() {}

	g.Go("network", func(ctx context.Context) error {
		defer func() {
			cancelDNSLock.Lock()
			cancelDNS()
			cancelDNSLock.Unlock()
		}()
		return s.watchClusterInfo(ctx)
	})

	// At this point, we wait until the VIF is ready. It will be, shortly after
	// the first ClusterInfo is received from the traffic-manager. A timeout
	// is needed so that we don't wait forever on a traffic-manager that has
	// been terminated for some reason.
	wc, cancel := client.GetConfig(c).Timeouts.TimeoutContext(c, client.TimeoutTrafficManagerConnect)
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

	// Start the router and the DNS Service and wait for the context
	// to be done. Then shut things down in order. The following happens:
	// 1. The DNS worker terminates (it needs the TUN device to be alive while doing that)
	// 2. The TUN device is closed (by the stop method). This unblocks the routerWorker's pending read on the device.
	// 3. The routerWorker terminates.
	g.Go("dns", func(ctx context.Context) error {
		defer s.stop(c) // using group parent context
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
	}
	return nil
}

func (s *Session) stop(c context.Context) {
	if !atomic.CompareAndSwapInt32(&s.closing, 0, 1) {
		// Session already stopped (or is stopping)
		return
	}
	dlog.Debug(c, "Bringing down TUN-device")

	s.scout.Report(c, "incluster_dns_queries",
		scout.Entry{Key: "total", Value: s.dnsLookups},
		scout.Entry{Key: "failures", Value: s.dnsFailures})

	cc, cancel := context.WithTimeout(c, time.Second)
	defer cancel()
	go func() {
		s.handlers.CloseAll(cc)
		cancel()
	}()
	<-cc.Done()
	atomic.StoreInt32(&s.closing, 2)

	if s.tunVif != nil {
		cc, cancel := context.WithTimeout(dcontext.WithoutCancel(c), 1*time.Second)
		defer cancel()
		if err := s.tunVif.Close(cc); err != nil {
			dlog.Errorf(c, "unable to close %s: %v", s.tunVif.Device.Name(), err)
		}
	}
}

func (s *Session) SetSearchPath(ctx context.Context, paths []string, namespaces []string) {
	s.dnsServer.SetSearchPath(ctx, paths, namespaces)
}

func (s *Session) SetExcludes(ctx context.Context, excludes []string) {
	s.dnsServer.SetExcludes(excludes)
}

func (s *Session) SetMappings(ctx context.Context, mappings []*rpc.DNSMapping) {
	s.dnsServer.SetMappings(mappings)
}

func (s *Session) applyConfig(ctx context.Context) error {
	cfg, err := client.LoadConfig(ctx)
	if err != nil {
		return err
	}
	return client.MergeAndReplace(ctx, &s.config, cfg, true)
}

func (s *Session) Done() chan struct{} {
	return s.done
}
