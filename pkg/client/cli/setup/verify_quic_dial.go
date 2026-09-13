package setup

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"syscall" //nolint:depguard // "unix" don't work on windows
	"time"

	"github.com/quic-go/quic-go"

	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/telepresenceio/telepresence/v2/pkg/quicfwd"
	"github.com/telepresenceio/telepresence/v2/pkg/routing"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

// quicDialProbeTimeout bounds the reachability handshake attempt: short enough that a
// silently-dropping firewall or an unreachable node network cannot stall verification
// beyond it, mirroring the client's own quicDialTimeout (pkg/client/rootd/quic.go).
const quicDialProbeTimeout = 3 * time.Second

// quicDiagnosisStepTimeout bounds each of diagnoseQuicSilence's follow-up probes so a
// silently-dropping path cannot stall the overall check.
const quicDiagnosisStepTimeout = 2 * time.Second

// quicDialer attempts one QUIC handshake against addr, tearing the connection down again
// on success. Production wires quicGoDial; tests substitute a fake so the reachability
// classification is exercised without a real socket.
type quicDialer func(ctx context.Context, addr string, tlsConf *tls.Config) error

// quicDiagnoser attempts to explain a QUIC dial timeout, returning one sentence to append to
// the finding, or "" when nothing could be learned. Production wires diagnoseQuicSilence;
// tests substitute a fake so the timeout path is exercised without opening real sockets.
type quicDiagnoser func(ctx context.Context, addr string, serviceType core.ServiceType) string

// quicGoDial is the production quicDialer. It skips certificate verification: the
// manager's CA is generated per-session and unknown to this probe, and the only question
// being asked is whether a UDP peer answers at all, not whether it is authentic.
func quicGoDial(ctx context.Context, addr string, tlsConf *tls.Config) error {
	conn, err := quic.DialAddr(ctx, addr, tlsConf, nil)
	if err != nil {
		return err
	}
	_ = conn.CloseWithError(0, "")
	return nil
}

// quicDialTLSConfig is the client TLS configuration for the reachability probe: the
// forwarder routes a connection's first packet by its SNI, so the traffic-manager's name
// must be present, and the same ALPN the manager negotiates for the real tunnel. Verification
// is disabled for the reason given on quicGoDial.
func quicDialTLSConfig() *tls.Config {
	return &tls.Config{ //nolint:gosec // reachability only, see quicGoDial
		InsecureSkipVerify: true,
		ServerName:         quicfwd.ManagerSNI,
		NextProtos:         []string{tunnel.QuicALPN},
	}
}

// verifyQuicReachability attempts a single QUIC handshake to the address the existence
// check already confirmed (an assigned LoadBalancer ingress or the allocated NodePort on
// a reachable node), replacing the existence finding with the reachability verdict.
func verifyQuicReachability(ctx context.Context, ki kubernetes.Interface, svc *core.Service, dial quicDialer, diagnose quicDiagnoser) Finding {
	addr, err := quicDialAddr(ctx, ki, svc)
	if err != nil {
		return Finding{Verdict: VerdictUnknown, Evidence: []string{fmt.Sprintf(
			"the QUIC endpoint exists but its dial address could not be determined: %v", err)}}
	}
	return quicReachabilityFinding(ctx, dial, diagnose, addr, svc.Spec.Type)
}

// quicReachabilityFinding classifies one dial attempt to addr: a completed handshake or any
// protocol-level rejection proves a peer answered over UDP; an explicit refusal or a timeout
// both prove silence, though a refusal at least confirms the host itself is up; anything else
// (e.g. a resolve failure) is inconclusive.
func quicReachabilityFinding(
	ctx context.Context, dial quicDialer, diagnose quicDiagnoser, addr string, serviceType core.ServiceType,
) Finding {
	dctx, cancel := context.WithTimeout(ctx, quicDialProbeTimeout)
	defer cancel()
	err := dial(dctx, addr, quicDialTLSConfig())
	switch {
	case err == nil, isQuicPeerResponse(err):
		return Finding{Verdict: VerdictYes, Evidence: []string{
			"QUIC endpoint reachable from this workstation at " + addr,
		}}
	case isConnRefused(err):
		_, port, _ := net.SplitHostPort(addr)
		return Finding{Verdict: VerdictNo, Evidence: []string{fmt.Sprintf(
			"QUIC endpoint %s not reachable over UDP from this workstation; clients fall back to gRPC. The host answers "+
				"but reports nothing listening on UDP port %s: the LoadBalancer or node does not forward UDP to the "+
				"traffic-manager.", addr, port)}}
	case errors.Is(err, context.DeadlineExceeded):
		evidence := fmt.Sprintf(
			"QUIC endpoint %s not reachable over UDP from this workstation; clients fall back to gRPC.", addr)
		if sentence := diagnose(ctx, addr, serviceType); sentence != "" {
			evidence += " " + sentence
		}
		return Finding{Verdict: VerdictNo, Evidence: []string{evidence}}
	default:
		return Finding{Verdict: VerdictUnknown, Evidence: []string{fmt.Sprintf(
			"could not attempt a QUIC dial to %s: %v", addr, err)}}
	}
}

// isQuicPeerResponse reports whether err is a protocol-level rejection that only a live
// peer can produce -- a TLS alert, an application close, a version mismatch, a stateless
// reset -- as opposed to silence.
func isQuicPeerResponse(err error) bool {
	var te *quic.TransportError
	var ae *quic.ApplicationError
	var ve *quic.VersionNegotiationError
	var se *quic.StatelessResetError
	return errors.As(err, &te) || errors.As(err, &ae) || errors.As(err, &ve) || errors.As(err, &se)
}

// isConnRefused reports whether err carries the ICMP port-unreachable the kernel maps onto
// the connecting socket -- a live host explicitly rejecting the port -- as opposed to
// silence.
func isConnRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED)
}

// diagnoseQuicSilence is the production quicDiagnoser. A TCP connect to the same host:port
// tells a blocked UDP path (the host answers) apart from an unreachable host (the host is
// silent on both protocols); for the latter it adds which interface or gateway would carry
// traffic there, when that can be determined.
func diagnoseQuicSilence(ctx context.Context, addr string, serviceType core.ServiceType) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	tctx, cancel := context.WithTimeout(ctx, quicDiagnosisStepTimeout)
	conn, dialErr := (&net.Dialer{}).DialContext(tctx, "tcp", addr)
	cancel()
	if dialErr == nil {
		_ = conn.Close()
	}
	if dialErr == nil || isConnRefused(dialErr) {
		return "The host answers on TCP but not on UDP, so the network path works and UDP port " + port +
			" is dropped on the way: check " + udpForwarder(serviceType, port) +
			" and any firewall between this workstation and the cluster."
	}
	sentence := "The host does not answer on TCP either, so it is unreachable from this workstation"
	if ip, perr := netip.ParseAddr(host); perr == nil {
		rctx, rcancel := context.WithTimeout(ctx, quicDiagnosisStepTimeout)
		table, rerr := routing.GetRoutingTable(rctx)
		rcancel()
		if rerr == nil {
			sentence += routeClauseForDest(table, ip)
		}
	}
	return sentence + "."
}

// routeClauseForDest picks the route in table that would carry traffic to dst -- the longest
// matching non-default prefix, or the default route otherwise -- and renders it as the clause
// to append to a diagnosis sentence. Empty when no route in table applies.
func routeClauseForDest(table []*routing.Route, dst netip.Addr) string {
	var best, def *routing.Route
	for _, r := range table {
		if r.Default {
			if def == nil {
				def = r
			}
			continue
		}
		if r.Routes(dst) && (best == nil || r.RoutedNet.Bits() > best.RoutedNet.Bits()) {
			best = r
		}
	}
	route := best
	if route == nil {
		route = def
	}
	if route == nil {
		return ""
	}
	clause := ", which routes it via interface " + route.InterfaceName
	switch {
	case strings.HasPrefix(route.InterfaceName, "docker") || strings.HasPrefix(route.InterfaceName, "br-"):
		clause += " (a Docker bridge; a kind cluster's node network is only reachable from the host that runs Docker)"
	case route.Default && route.Gateway.IsValid() && !route.Gateway.IsUnspecified():
		clause += " via the default gateway " + route.Gateway.String()
	}
	return clause
}

// quicPortName is the chart's fixed name for the QUIC Service's port.
const quicPortName = "quic"

// quicDialAddr resolves the address to dial for the reachability probe from the QUIC
// Service's already-confirmed endpoint: a LoadBalancer's ingress address, or a NodePort
// together with a node address (ExternalIP preferred, InternalIP as fallback).
func quicDialAddr(ctx context.Context, ki kubernetes.Interface, svc *core.Service) (string, error) {
	return resolveServiceDialAddr(ctx, ki, svc, quicPortName, "the QUIC service")
}

// firstNodeAddress lists the cluster's nodes and returns the first ExternalIP found,
// falling back to the first InternalIP when no node has one. Listing nodes requires
// cluster-wide RBAC that a namespace-scoped install may lack; that denial is returned as
// an error for the caller to render as an inconclusive finding, not a probe failure.
func firstNodeAddress(ctx context.Context, ki kubernetes.Interface) (string, error) {
	nodes, err := ki.CoreV1().Nodes().List(ctx, meta.ListOptions{})
	if err != nil {
		return "", err
	}
	var internal string
	for _, n := range nodes.Items {
		for _, a := range n.Status.Addresses {
			switch a.Type {
			case core.NodeExternalIP:
				if a.Address != "" {
					return a.Address, nil
				}
			case core.NodeInternalIP:
				if internal == "" && a.Address != "" {
					internal = a.Address
				}
			}
		}
	}
	if internal == "" {
		return "", errors.New("no node has a usable address")
	}
	return internal, nil
}

// udpForwarder names what should be forwarding UDP traffic on port to the
// traffic-manager for a Service of the given type.
func udpForwarder(serviceType core.ServiceType, port string) string {
	switch serviceType {
	case core.ServiceTypeLoadBalancer:
		return "the LoadBalancer's UDP forwarding"
	case core.ServiceTypeNodePort:
		return "that the node forwards UDP node port " + port + " to the traffic-manager"
	default:
		return "that UDP port " + port + " is forwarded to the traffic-manager"
	}
}
