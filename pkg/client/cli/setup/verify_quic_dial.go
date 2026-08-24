package setup

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"time"

	"github.com/quic-go/quic-go"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

// quicDialProbeTimeout bounds the reachability handshake attempt: short enough that a
// silently-dropping firewall or an unreachable node network cannot stall verification
// beyond it, mirroring the client's own quicDialTimeout (pkg/client/rootd/quic.go).
const quicDialProbeTimeout = 3 * time.Second

// quicDialer attempts one QUIC handshake against addr, tearing the connection down again
// on success. Production wires quicGoDial; tests substitute a fake so the reachability
// classification is exercised without a real socket.
type quicDialer func(ctx context.Context, addr string, tlsConf *tls.Config) error

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

// quicDialTLSConfig is the client TLS configuration for the reachability probe: the same
// ALPN the manager negotiates for the real tunnel, with verification disabled for the
// reason given on quicGoDial.
func quicDialTLSConfig() *tls.Config {
	return &tls.Config{InsecureSkipVerify: true, NextProtos: []string{tunnel.QuicALPN}} //nolint:gosec // reachability only, see quicGoDial
}

// verifyQuicReachability attempts a single QUIC handshake to the address the existence
// check already confirmed (an assigned LoadBalancer ingress or the allocated NodePort on
// a reachable node), replacing the existence finding with the reachability verdict.
func verifyQuicReachability(ctx context.Context, ki kubernetes.Interface, svc *corev1.Service, dial quicDialer) Finding {
	addr, err := quicDialAddr(ctx, ki, svc)
	if err != nil {
		return Finding{Verdict: VerdictUnknown, Evidence: []string{fmt.Sprintf(
			"the QUIC endpoint exists but its dial address could not be determined: %v", err)}}
	}
	return quicReachabilityFinding(ctx, dial, addr)
}

// quicReachabilityFinding classifies one dial attempt to addr: a completed handshake or
// any protocol-level rejection proves a peer answered over UDP (the manager's CA is
// session-local, so a certificate error is exactly as conclusive as success); a timeout
// proves the opposite; anything else (e.g. a resolve failure) is inconclusive.
func quicReachabilityFinding(ctx context.Context, dial quicDialer, addr string) Finding {
	dctx, cancel := context.WithTimeout(ctx, quicDialProbeTimeout)
	defer cancel()
	err := dial(dctx, addr, quicDialTLSConfig())
	switch {
	case err == nil, isQuicPeerResponse(err):
		return Finding{Verdict: VerdictYes, Evidence: []string{
			"QUIC endpoint reachable from this workstation at " + addr,
		}}
	case errors.Is(err, context.DeadlineExceeded):
		return Finding{Verdict: VerdictNo, Evidence: []string{fmt.Sprintf(
			"QUIC endpoint %s not reachable over UDP from this workstation — clients will silently fall back to gRPC "+
				"(likely causes: a firewall between here and the cluster, the LoadBalancer still provisioning, or the "+
				"node network being unreachable from this host, e.g. kind's docker network)", addr)}}
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

// quicPortName is the chart's fixed name for the QUIC Service's port.
const quicPortName = "quic"

// quicDialAddr resolves the address to dial for the reachability probe from the QUIC
// Service's already-confirmed endpoint: a LoadBalancer's ingress address, or a NodePort
// together with a node address (ExternalIP preferred, InternalIP as fallback).
func quicDialAddr(ctx context.Context, ki kubernetes.Interface, svc *corev1.Service) (string, error) {
	return resolveServiceDialAddr(ctx, ki, svc, quicPortName, "the QUIC service")
}

// firstNodeAddress lists the cluster's nodes and returns the first ExternalIP found,
// falling back to the first InternalIP when no node has one. Listing nodes requires
// cluster-wide RBAC that a namespace-scoped install may lack; that denial is returned as
// an error for the caller to render as an inconclusive finding, not a probe failure.
func firstNodeAddress(ctx context.Context, ki kubernetes.Interface) (string, error) {
	nodes, err := ki.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", err
	}
	var internal string
	for _, n := range nodes.Items {
		for _, a := range n.Status.Addresses {
			switch a.Type {
			case corev1.NodeExternalIP:
				if a.Address != "" {
					return a.Address, nil
				}
			case corev1.NodeInternalIP:
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
