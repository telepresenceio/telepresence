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
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blang/semver"
	dns2 "github.com/miekg/dns"
	"github.com/puzpuzpuz/xsync/v3"
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
	"k8s.io/client-go/kubernetes"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/datawire/k8sapi/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/agentpf"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8sclient"
	"github.com/telepresenceio/telepresence/v2/pkg/client/rootd/dns"
	"github.com/telepresenceio/telepresence/v2/pkg/client/rootd/vip"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/client/socket"
	"github.com/telepresenceio/telepresence/v2/pkg/dnet"
	"github.com/telepresenceio/telepresence/v2/pkg/dnsproxy"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/slice"
	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/vif"
)

type agentSubnet struct {
	net.IPNet
	workload string
}

type agentVIP struct {
	workload      string
	destinationIP net.IP
}

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
	tunVif *vif.TunnelingDevice

	// clientConn is the connection that uses the connector's socket
	clientConn *grpc.ClientConn

	// agentClients provides the gRPC tunnel to traffic-agents in the connected namespace
	agentClients agentpf.Clients

	// managerClient provides the gRPC tunnel to the traffic-manager
	managerClient connector.ManagerProxyClient

	// managerVersion is the version of the connected traffic-manager
	managerVersion semver.Version

	// namespace that the client is connected to
	namespace string

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

	// serviceSubnet reported by the traffic-manager
	serviceSubnet *net.IPNet

	// podSubnets reported by the traffic-manager
	podSubnets []*net.IPNet

	// Subnets configured by the user
	alsoProxySubnets []*net.IPNet

	// Subnets configured by the user to never be proxied
	neverProxySubnets []*net.IPNet

	// Subnets that will be mapped even if they conflict with local routes
	allowConflictingSubnets []*net.IPNet

	// localTranslationTable maps an IP returned by the cluster's DNS to a virtual IP created by this server.
	localTranslationTable *xsync.MapOf[iputil.IPKey, net.IP]

	// IP addresses that the cluster's DNS resolves that are contained in one of the subnets in this
	// slice are translated to a virtual IP (cached in the localTranslationTable)
	localTranslationSubnets []agentSubnet

	// virtualIPs maps a virtual IP to an agent tunnel.
	virtualIPs *xsync.MapOf[iputil.IPKey, agentVIP]

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
	dnsServerSubnet *net.IPNet

	// vifReady is closed when the virtual network interface has been configured.
	vifReady chan error

	// config is the session config given by the traffic manager
	config client.Config

	// done is closed when the session ends
	done               chan struct{}
	subnetViaWorkloads []*rpc.SubnetViaWorkload
}

type NewSessionFunc func(context.Context, *rpc.OutboundInfo) (context.Context, *Session, error)

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

func createPortForwardDialer(ctx context.Context, kubeFlags map[string]string, kubeData []byte) (dnet.PortForwardDialer, kubernetes.Interface, error) {
	configFlags, err := client.ConfigFlags(kubeFlags)
	if err != nil {
		return nil, nil, err
	}
	config, err := client.NewClientConfig(ctx, configFlags, kubeData)
	if err != nil {
		return nil, nil, err
	}
	rs, err := config.ClientConfig()
	if err != nil {
		return nil, nil, err
	}
	cs, err := kubernetes.NewForConfig(rs)
	if err != nil {
		return nil, nil, err
	}
	pfDialer, err := dnet.NewK8sPortForwardDialer(ctx, rs, cs)
	return pfDialer, cs, err
}

// connectToManager connects to the traffic-manager and asserts that its version is compatible.
func connectToManager(
	ctx context.Context,
	namespace string,
	kubeFlags map[string]string,
	kubeData []byte,
) (
	context.Context,
	*grpc.ClientConn,
	connector.ManagerProxyClient,
	semver.Version,
	error,
) {
	if !client.GetConfig(ctx).Cluster().ConnectFromRootDaemon {
		conn, mp, v, err := connectToUserDaemon(ctx)
		return ctx, conn, mp, v, err
	}
	var mgrVer semver.Version
	pfDialer, cs, err := createPortForwardDialer(ctx, kubeFlags, kubeData)
	if err != nil {
		return ctx, nil, nil, mgrVer, err
	}
	ctx = k8sapi.WithK8sInterface(ctx, cs)

	clientConfig := client.GetConfig(ctx)
	tos := clientConfig.Timeouts()

	timedCtx, cancel := tos.TimeoutContext(ctx, client.TimeoutTrafficManagerConnect)
	defer cancel()
	conn, mc, ver, err := k8sclient.ConnectToManager(timedCtx, namespace, pfDialer.Dial)
	if err != nil {
		return ctx, nil, nil, mgrVer, err
	}

	verStr := strings.TrimPrefix(ver.Version, "v")
	dlog.Infof(ctx, "Connected to Manager %s", verStr)
	mgrVer, err = semver.Parse(verStr)
	if err != nil {
		conn.Close()
		return ctx, nil, nil, mgrVer, fmt.Errorf("failed to parse manager version %q: %w", verStr, err)
	}
	return dnet.WithPortForwardDialer(ctx, pfDialer), conn, &userdToManagerShortcut{mc}, mgrVer, nil
}

// connectToUserDaemon is like connectToManager but the port-forward will be established from the user-daemon
// instead. This doesn't matter when the daemon is containerized, but it will introduce an extra hop for all
// outgoing traffic when it isn't.
func connectToUserDaemon(c context.Context) (*grpc.ClientConn, connector.ManagerProxyClient, semver.Version, error) {
	// First check. Establish connection
	tos := client.GetConfig(c).Timeouts()
	tc, cancel := tos.TimeoutContext(c, client.TimeoutTrafficManagerAPI)
	defer cancel()

	var conn *grpc.ClientConn
	conn, err := socket.Dial(tc, socket.UserDaemonPath(c),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
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
func NewSession(c context.Context, mi *rpc.OutboundInfo) (context.Context, *Session, error) {
	dlog.Info(c, "-- Starting new session")

	c, conn, mc, ver, err := connectToManager(c, mi.ManagerNamespace, mi.KubeFlags, mi.KubeconfigData)
	if mc == nil || err != nil {
		return c, nil, err
	}
	s, err := newSession(c, mi, mc, ver)
	if err != nil {
		return c, nil, err
	}
	s.clientConn = conn
	// store session in ctx for reporting
	c = scout.WithSession(c, s)
	return c, s, nil
}

func nope() bool { return false }

func newSession(c context.Context, mi *rpc.OutboundInfo, mc connector.ManagerProxyClient, ver semver.Version) (*Session, error) {
	cfg := client.GetDefaultConfig()
	cliCfg, err := mc.GetClientConfig(c, &empty.Empty{})
	if err != nil {
		dlog.Warnf(c, "Failed to get remote config from traffic manager: %v", err)
	} else {
		err = yaml.Unmarshal(cliCfg.ConfigYaml, cfg)
		if err != nil {
			dlog.Warnf(c, "Failed to deserialize remote config: %v", err)
		}
	}
	dlog.Debugf(c, "Creating session with id %v", mi.Session)

	s := &Session{
		handlers:           tunnel.NewPool(),
		rndSource:          rand.NewSource(time.Now().UnixNano()),
		session:            mi.Session,
		namespace:          mi.Namespace,
		managerClient:      mc,
		managerVersion:     ver,
		subnetViaWorkloads: mi.SubnetViaWorkloads,
		proxyClusterPods:   true,
		proxyClusterSvcs:   true,
		vifReady:           make(chan error, 2),
		config:             cfg,
		done:               make(chan struct{}),
	}
	s.alsoProxySubnets, err = validateSubnets("also-proxy", mi.AlsoProxySubnets, s.alsoProxyVia)
	if err != nil {
		return nil, err
	}
	dlog.Infof(c, "also-proxy subnets %v", s.alsoProxySubnets)

	s.neverProxySubnets, err = validateSubnets("never-proxy", mi.NeverProxySubnets, nope)
	if err != nil {
		return nil, err
	}
	dlog.Infof(c, "never-proxy subnets %v", s.neverProxySubnets)

	s.allowConflictingSubnets, err = validateSubnets("allow-conflicting", mi.AllowConflictingSubnets, nope)
	if err != nil {
		return nil, err
	}
	dlog.Infof(c, "allow-conflicting subnets %v", s.allowConflictingSubnets)

	s.dnsServer = dns.NewServer(mi.Dns, s.clusterLookup)
	s.SetSearchPath(c, nil, nil)
	return s, nil
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
		rCode := dns2.RcodeServerFailure
		switch status.Code(err) {
		case codes.Unavailable, codes.DeadlineExceeded:
			rCode = dns2.RcodeNameError
			err = nil
		}
		return nil, rCode, err
	}
	answer, rCode, err := dnsproxy.FromRPC(r)
	if err != nil {
		s.dnsFailures++
		return nil, dns2.RcodeServerFailure, err
	}
	if len(s.localTranslationSubnets) > 0 {
		for _, rr := range answer {
			switch rr := rr.(type) {
			case *dns2.A:
				rr.A, err = s.maybeGetVirtualIP(ctx, rr.A)
			case *dns2.AAAA:
				rr.AAAA, err = s.maybeGetVirtualIP(ctx, rr.AAAA)
			}
			if err != nil {
				rCode = dns2.RcodeServerFailure
				break
			}
		}
	}
	return answer, rCode, err
}

func (s *Session) maybeGetVirtualIP(ctx context.Context, destinationIP net.IP) (net.IP, error) {
	var err error
	vip, ok := s.localTranslationTable.Compute(iputil.IPKey(destinationIP), func(existing net.IP, loaded bool) (net.IP, bool) {
		if loaded {
			return existing, false
		}
		for _, sn := range s.localTranslationSubnets {
			if sn.Contains(destinationIP) {
				var nip net.IP
				nip, err = s.nextVirtualIP(sn.workload, destinationIP)
				return nip, err != nil
			}
		}
		return nil, true
	})
	if ok {
		dlog.Debugf(ctx, "using VIP %q for resolved IP %q", vip, destinationIP)
		destinationIP = vip
	}
	return destinationIP, err
}

func (s *Session) nextVirtualIP(workload string, destinationIP net.IP) (net.IP, error) {
	vip, err := s.vipGenerator.Next()
	if err != nil {
		return nil, err
	}
	s.virtualIPs.Store(iputil.IPKey(vip), agentVIP{workload: workload, destinationIP: destinationIP})
	return vip, nil
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
	if len(s.allowConflictingSubnets) > 0 {
		info.AllowConflictingSubnets = make([]*manager.IPNet, len(s.allowConflictingSubnets))
		for i, np := range s.allowConflictingSubnets {
			info.AllowConflictingSubnets[i] = iputil.IPNetToRPC(np)
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

// shouldProxySubnet returns true unless the given subnet is covered by a subnet in the neverProxySubnets list.
func (s *Session) shouldProxySubnet(ctx context.Context, name string, sn *net.IPNet) bool {
	if sn.IP.IsLoopback() {
		dlog.Infof(ctx, "Will not proxy %s subnet %s, because it is loopback", name, sn)
		return false
	}
	for _, lt := range s.localTranslationSubnets {
		if subnet.Covers(&lt.IPNet, sn) {
			dlog.Infof(ctx, "Will not proxy %s subnet %s, because it covered by --proxy-via %s=%s", name, sn, lt.IPNet, lt.workload)
			return false
		}
	}
	for _, nps := range s.neverProxySubnets {
		if subnet.Covers(nps, sn) {
			// Allow if there's an also-proxy that is smaller, contradicting the never-proxy
			for _, aps := range s.alsoProxySubnets {
				if subnet.Covers(nps, aps) && subnet.Covers(aps, sn) {
					dlog.Infof(ctx, "Will proxy %s subnet %s, because it is covered by also-proxy %s overriding never-proxy %s", name, sn, nps, aps)
					return true
				}
			}
			dlog.Infof(ctx, "Will not proxy %s subnet %s, because it is covered by never-proxy %s", name, sn, nps)
			return false
		}
	}
	for _, npx := range s.subnetViaWorkloads {
		if name == "service" && npx.Subnet == "service" || name == "pod" && npx.Subnet == "pods" {
			dlog.Infof(ctx, "Will not proxy %s subnet %s, because it is covered by --proxy-via %s=%s", name, sn, npx.Subnet, npx.Workload)
			return false
		}
	}
	return true
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
			if err = s.readAdditionalRouting(ctx, mgrInfo); err != nil {
				return err
			}
			select {
			case <-s.vifReady:
				if err := s.onClusterInfo(ctx, mgrInfo, span); err != nil {
					if !errors.Is(err, context.Canceled) {
						dlog.Error(ctx, err)
					}
					return err
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
	span.SetAttributes(
		attribute.Bool("tel2.proxy-svcs", s.proxyClusterSvcs),
		attribute.Bool("tel2.proxy-pods", s.proxyClusterPods),
	)
	return s.onClusterInfo(ctx, mgrInfo, span)
}

func (s *Session) onClusterInfo(ctx context.Context, mgrInfo *manager.ClusterInfo, span trace.Span) error {
	dlog.Debugf(ctx, "WatchClusterInfo update")
	if mgrInfo.Dns == nil {
		// Older traffic-manager. Use deprecated mgrInfo fields for DNS
		mgrInfo.Dns = &manager.DNS{
			ClusterDomain: mgrInfo.ClusterDomain,
		}
	}
	if mgrInfo.Routing == nil {
		mgrInfo.Routing = &manager.Routing{}
	}

	s.serviceSubnet = nil
	s.podSubnets = nil

	var subnets []*net.IPNet
	if s.proxyClusterSvcs {
		if mgrInfo.ServiceSubnet != nil {
			cidr := iputil.IPNetFromRPC(mgrInfo.ServiceSubnet)
			if s.shouldProxySubnet(ctx, "service", cidr) {
				dlog.Infof(ctx, "Adding service subnet %s", cidr)
				subnets = append(subnets, cidr)
			}
			s.serviceSubnet = cidr
		}
	}

	if s.proxyClusterPods {
		for _, sn := range mgrInfo.PodSubnets {
			cidr := iputil.IPNetFromRPC(sn)
			if s.shouldProxySubnet(ctx, "pod", cidr) {
				dlog.Infof(ctx, "Adding pod subnet %s", cidr)
				subnets = append(subnets, cidr)
			}
			s.podSubnets = append(s.podSubnets, cidr)
		}
	}

	if s.vipGenerator != nil {
		subnets = append(subnets, s.vipGenerator.Subnet())
		dlog.Debugf(ctx, "Adding VIP subnet %q to TUN-device", s.vipGenerator.Subnet().String())
		s.consolidateProxyViaWorkloads(ctx)
	}

	if !s.alsoProxyVia() {
		subnets = append(subnets, s.alsoProxySubnets...)
	}

	// We use the ManagerPodIp as the dnsIP. The reason for this is that no one should ever
	// talk to the traffic-manager directly using the TUN device, so it's safe to use its
	// IP to impersonate the DNS server. All traffic sent to that IP, will be routed to
	// the local DNS server.
	dnsIP := net.IP(mgrInfo.ManagerPodIp)
	dnsRouted := false
	for _, sn := range subnets {
		if sn.Contains(dnsIP) {
			dnsRouted = true
			break
		}
	}
	if runtime.GOOS != "darwin" && !dnsRouted {
		// We'll need to synthesize a subnet where we can attach the DNS service when the VIF isn't configured
		// from cluster subnets. But not on darwin systems, because there the DNS is controlled by /etc/resolver
		// entries appointing the DNS service directly via localhost:<port>.
		if s.vipGenerator != nil {
			var err error
			dnsIP, err = s.vipGenerator.Next()
			if err != nil {
				return nil
			}
		} else {
			if s.dnsServerSubnet == nil {
				s.createSubnetForDNSOnly(ctx, mgrInfo)
			}
			dlog.Infof(ctx, "Adding Service subnet %s (for DNS only)", s.dnsServerSubnet)
			subnets = append(subnets, s.dnsServerSubnet)
			dnsIP = make(net.IP, len(s.dnsServerSubnet.IP))
			copy(dnsIP, s.dnsServerSubnet.IP)
			dnsIP[len(dnsIP)-1] = 2
		}
		dnsRouted = true
	}

	if len(subnets) > 0 && s.tunVif == nil {
		var err error
		if s.tunVif, err = vif.NewTunnelingDevice(ctx, s.streamCreator()); err != nil {
			return fmt.Errorf("NewTunnelVIF: %w", err)
		}
	}

	if dnsRouted {
		d := mgrInfo.Dns
		dlog.Infof(ctx, "Setting cluster DNS to %s", dnsIP)
		dlog.Infof(ctx, "Setting cluster domain to %q", d.ClusterDomain)
		s.dnsServer.SetClusterDNS(d, dnsIP)
		span.SetAttributes(
			attribute.Stringer("tel2.cluster-dns", dnsIP),
			attribute.String("tel2.cluster-domain", d.ClusterDomain),
		)
	}

	proxy, neverProxy, neverProxyOverrides := computeNeverProxyOverrides(ctx, subnets, s.neverProxySubnets)

	// Fire and forget to send metrics out.
	go func() {
		scout.Report(ctx, "update_routes",
			scout.Entry{Key: "subnets", Value: len(proxy)},
			scout.Entry{Key: "allow_conflicting_subnets", Value: len(s.allowConflictingSubnets)},
		)
	}()
	if s.tunVif == nil {
		return nil
	}
	rt := s.tunVif.Router
	rt.UpdateWhitelist(s.allowConflictingSubnets)
	return rt.UpdateRoutes(ctx, proxy, neverProxy, neverProxyOverrides)
}

func computeNeverProxyOverrides(ctx context.Context, subnets, nvp []*net.IPNet) (proxy, neverProxy, neverProxyOverrides []*net.IPNet) {
	neverProxy = slices.Clone(nvp)
	last := len(neverProxy) - 1
	for i := 0; i <= last; {
		nps := neverProxy[i]
		found := false
		for _, ds := range subnets {
			if subnet.Overlaps(ds, nps) {
				found = true
				break
			}
		}
		if !found {
			// This never-proxy is pointless because it's not a subnet that we are routing
			dlog.Infof(ctx, "Dropping never-proxy %q because it is not routed", nps)
			if last > i {
				neverProxy[i] = neverProxy[last]
			}
			last--
		} else {
			i++
		}
	}
	neverProxy = neverProxy[:last+1]

	proxy, neverProxyOverrides = subnet.Partition(subnets, func(i int, isn *net.IPNet) bool {
		for r, rsn := range subnets {
			if i == r {
				continue
			}
			if subnet.Covers(rsn, isn) && !subnet.Equal(rsn, isn) {
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

func validateSubnets(name string, sns []*manager.IPNet, allowLoopback func() bool) ([]*net.IPNet, error) {
	ns := iputil.ConvertSubnets(sns)
	if len(ns) == 0 {
		return nil, nil
	}
	ns = subnet.Unique(ns)
	rs := make([]*net.IPNet, 0, len(ns))
	for _, sn := range ns {
		if sn.IP.IsLoopback() && !allowLoopback() {
			return nil, fmt.Errorf(`%s subnet %s is a loopback subnet. It is never proxied`, name, sn)
		}
		rs = append(rs, sn)
	}
	return subnet.Unique(rs), nil
}

// alsoProxyVia will return true when the connection was made using --subnet-via all=<workload> or --subnet-via also=<workload>.
func (s *Session) alsoProxyVia() bool {
	for _, pvx := range s.subnetViaWorkloads {
		if pvx.Subnet == "also" { // no need to test for "all". It's normalized into ["also", "pods", "service"]
			return true
		}
	}
	return false
}

func (s *Session) readAdditionalRouting(ctx context.Context, mgrInfo *manager.ClusterInfo) error {
	if r := mgrInfo.Routing; r != nil {
		sns, err := validateSubnets("also-proxy", r.AlsoProxySubnets, s.alsoProxyVia)
		if err != nil {
			return err
		}
		s.alsoProxySubnets = subnet.Unique(append(s.alsoProxySubnets, sns...))
		dlog.Infof(ctx, "also-proxy subnets %v", s.alsoProxySubnets)

		sns, err = validateSubnets("never-proxy", r.NeverProxySubnets, nope)
		if err != nil {
			return err
		}
		s.neverProxySubnets = subnet.Unique(append(s.neverProxySubnets, sns...))
		dlog.Infof(ctx, "never-proxy subnets %v", s.neverProxySubnets)

		sns, err = validateSubnets("allow-conflicting", r.AllowConflictingSubnets, nope)
		if err != nil {
			return err
		}
		s.allowConflictingSubnets = subnet.Unique(append(s.allowConflictingSubnets, sns...))
		dlog.Infof(ctx, "allow-conflicting subnets %v", s.allowConflictingSubnets)
	}
	return nil
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
	ct := client.GetConfig(ctx).Timeouts().Get(client.TimeoutConnectivityCheck)
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
	url := iputil.JoinHostPort(ip, uint16(port))
	url = fmt.Sprintf("https://%s/healthz", url)
	request, err := http.NewRequestWithContext(tCtx, http.MethodGet, url, nil)
	if err != nil {
		if ctx.Err() != nil {
			return false // parent context cancelled
		}
		// As far as I can tell, this error means a) that the context was cancelled before the request could be allocated, or b) that the request is misconstructed, e.g. bad method.
		// Neither of those two should really happen here (unless you set the timeout to a few microseconds, maybe), but we can't really continue. May as well route the cluster.
		dlog.Errorf(ctx, "Unexpected: service conn check could not build request: %v. Will route services anyway.", err)
		return true
	}
	request.Header.Set("Host", info.InjectorSvcHost)
	dlog.Debugf(ctx, "Performing service connectivity check on %s with Host %s and timeout %s", url, info.InjectorSvcHost, ct)
	resp, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return false // parent context cancelled
		}
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
	ct := client.GetConfig(ctx).Timeouts().Get(client.TimeoutConnectivityCheck)
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
	conn, err := grpc.DialContext(tCtx, net.JoinHostPort(ip, strconv.Itoa(int(port))), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		if ctx.Err() != nil {
			return false // parent context cancelled
		}
		dlog.Debugf(ctx, "Will proxy pods (%v)", err)
		return true
	}
	defer conn.Close()
	mClient := manager.NewManagerClient(conn)
	if _, err := mClient.Version(tCtx, &empty.Empty{}); err != nil {
		if ctx.Err() != nil {
			return false // parent context cancelled
		}
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

	if rmc, ok := s.managerClient.(interface{ RealManagerClient() manager.ManagerClient }); ok {
		clusterCfg := client.GetConfig(c).Cluster()
		if clusterCfg.AgentPortForward && clusterCfg.ConnectFromRootDaemon {
			if k8sclient.CanPortForward(c, s.namespace) {
				s.agentClients = agentpf.NewClients(s.session)
				g.Go("agentPods", func(ctx context.Context) error {
					if err := s.activateProxyViaWorkloads(c); err != nil {
						return err
					}
					return s.agentClients.WatchAgentPods(ctx, rmc.RealManagerClient())
				})
			} else {
				dlog.Infof(c, "Agent port-forwards are disabled. Client is not permitted to do port-forward to namespace %s", s.namespace)
			}
		}
	}

	if s.agentClients == nil && len(s.subnetViaWorkloads) > 0 {
		return fmt.Errorf("--proxy-via can only be used when cluster.agentPortForward is enabled")
	}

	// At this point, we wait until the VIF is ready. It will be, shortly after
	// the first ClusterInfo is received from the traffic-manager. A timeout
	// is needed so that we don't wait forever on a traffic-manager that has
	// been terminated for some reason.
	wc, cancel := client.GetConfig(c).Timeouts().TimeoutContext(c, client.TimeoutTrafficManagerConnect)
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
		return s.waitForProxyViaWorkloads(c)
	}
	return nil
}

func (s *Session) stop(c context.Context) {
	if !atomic.CompareAndSwapInt32(&s.closing, 0, 1) {
		// Session already stopped (or is stopping)
		return
	}
	dlog.Debug(c, "Bringing down TUN-device")

	scout.Report(c, "incluster_dns_queries",
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
		cc, cancel := context.WithTimeout(context.WithoutCancel(c), 1*time.Second)
		defer cancel()
		if err := s.tunVif.Close(cc); err != nil {
			dlog.Errorf(c, "unable to close %s: %v", s.tunVif.Device.Name(), err)
		}
	}
}

func (s *Session) activateProxyViaWorkloads(ctx context.Context) error {
	sl := len(s.subnetViaWorkloads)
	if sl == 0 {
		return nil
	}
	_, vipSubnet, err := net.ParseCIDR(client.GetConfig(ctx).Cluster().VirtualIPSubnet)
	if err != nil {
		return fmt.Errorf("unable to parse configuration value cluster.virtualIPSubnet: %w", err)
	}
	s.vipGenerator = vip.NewGenerator(vipSubnet)
	s.localTranslationTable = xsync.NewMapOf[iputil.IPKey, net.IP]()
	s.virtualIPs = xsync.NewMapOf[iputil.IPKey, agentVIP]()
	s.localTranslationSubnets = make([]agentSubnet, sl)
	for _, wlName := range s.consolidateProxyViaWorkloads(ctx) {
		dlog.Debugf(ctx, "Ensuring proxy-via agent in %s", wlName)
		_, err = s.managerClient.EnsureAgent(ctx, &manager.EnsureAgentRequest{
			Session: s.session,
			Name:    wlName,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Session) consolidateProxyViaWorkloads(ctx context.Context) []string {
	desiredVips := make(map[string][]*net.IPNet)
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
			if s.serviceSubnet != nil {
				desiredVips[pvx.Workload] = append(desiredVips[pvx.Workload], s.serviceSubnet)
				snCount++
			}
		default:
			_, sn, err := net.ParseCIDR(pvx.Subnet)
			if err != nil {
				dlog.Warnf(ctx, "unable to parse proxy-via subnet %s", pvx.Subnet)
			} else {
				desiredVips[pvx.Workload] = append(desiredVips[pvx.Workload], sn)
				snCount++
			}
		}
	}

	wlNames := make([]string, len(desiredVips))
	lcs := make([]agentSubnet, 0, snCount)
	i := 0
	for wlName, sns := range desiredVips {
		wlNames[i] = wlName
		i++
		for _, sn := range sns {
			lcs = append(lcs, agentSubnet{IPNet: *sn, workload: wlName})
		}
	}
	s.localTranslationSubnets = lcs
	return wlNames
}

func (s *Session) waitForProxyViaWorkloads(ctx context.Context) error {
	wc := len(s.subnetViaWorkloads)
	if wc == 0 {
		return nil
	}
	to := client.GetConfig(ctx).Timeouts().Get(client.TimeoutIntercept)
	waitCh := make(chan error)

	// Need unique workload names
	ws := make([]string, 0, len(s.subnetViaWorkloads))
	for _, svw := range s.subnetViaWorkloads {
		ws = slice.AppendUnique(ws, svw.Workload)
	}
	for _, wl := range ws {
		s.agentClients.SetProxyVia(wl)
		dlog.Debugf(ctx, "Waiting for proxy-via agent in %s", wl)
		go func(wl string) {
			waitCh <- s.agentClients.WaitForWorkload(ctx, to, wl)
		}(wl)
	}
	for _, wl := range ws {
		select {
		case <-ctx.Done():
			return nil
		case err := <-waitCh:
			if err != nil {
				return fmt.Errorf("proxy-via agent in %s failed: %w", wl, err)
			}
			dlog.Debugf(ctx, "Wait succeeded for proxy-via agent in %s", wl)
		}
	}
	return nil
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
	return client.MergeAndReplace(ctx, s.config, cfg, true)
}

func (s *Session) waitForAgentIP(ctx context.Context, request *rpc.WaitForAgentIPRequest) (*empty.Empty, error) {
	if s.agentClients == nil {
		return nil, status.Error(codes.Unavailable, "")
	}
	err := s.agentClients.WaitForIP(ctx, request.Timeout.AsDuration(), request.Ip)
	switch {
	case err == nil:
	case errors.Is(err, context.DeadlineExceeded):
		err = status.Error(codes.DeadlineExceeded, "")
	case errors.Is(err, context.Canceled):
		err = status.Error(codes.Canceled, "")
	default:
		err = status.Error(codes.Internal, err.Error())
	}
	return &empty.Empty{}, err
}

func (s *Session) Done() <-chan struct{} {
	return s.done
}

func (s *Session) ManagerVersion() semver.Version {
	return s.managerVersion
}
