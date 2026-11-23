package quic

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"

	quicgo "github.com/quic-go/quic-go"
	"google.golang.org/protobuf/proto"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

const (
	tunnelwireDialTimeout = 8 * time.Second
	tunnelwireReadTimeout = 60 * time.Second
	tunnelwireSettle      = 5 * time.Second
	tunnelwireReadyWait   = 120 * time.Second
	tunnelwireManagerSel  = "app=traffic-manager"
	tunnelwireManagerCtr  = "traffic-manager"
)

// tunnelwirePeer holds a manager session and the QUIC endpoint credentials it was given.
type tunnelwirePeer struct {
	session *manager.SessionInfo
	tlsConf *tls.Config
	addrs   []string
}

// tunnelwireArrive opens a raw manager session in ns and fetches its QUIC endpoint. The
// session is kept alive until the test ends.
func tunnelwireArrive(t testing.TB, ctx context.Context, r *rt.Runtime, ns string) *tunnelwirePeer {
	t.Helper()
	mc, closeMC, err := rt.ManagerClient(rt.Env{Ctx: ctx, T: t, R: r}, managers.ManagerNamespace)
	if err != nil {
		t.Fatalf("manager client: %v", err)
	}
	t.Cleanup(closeMC)
	si, err := mc.ArriveAsClient(ctx, &manager.ClientInfo{
		Name:      "rtest-tunnelwire",
		Namespace: ns,
		InstallId: "rtest-tunnelwire",
		Product:   "telepresence",
		Version:   r.Version().String(),
	})
	if err != nil {
		t.Fatalf("ArriveAsClient: %v", err)
	}
	keepCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_, _ = mc.Remain(keepCtx, &manager.RemainRequest{Session: si})
			case <-keepCtx.Done():
				return
			}
		}
	}()

	ep, err := mc.GetQuicTunnelEndpoint(ctx, si)
	if err != nil {
		t.Fatalf("GetQuicTunnelEndpoint: %v", err)
	}
	if !ep.GetEnabled() {
		t.Fatalf("the traffic-manager advertises no QUIC endpoint")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ep.CaPem) {
		t.Fatalf("no valid CA certificate in the QUIC endpoint")
	}
	cert, err := tls.X509KeyPair(ep.ClientCertPem, ep.ClientKeyPem)
	if err != nil {
		t.Fatalf("client certificate: %v", err)
	}
	alpn := ep.Alpn
	if alpn == "" {
		alpn = tunnel.QuicALPN
	}
	var addrs []string
	for _, c := range ep.Candidates {
		addrs = append(addrs, net.JoinHostPort(c.Host, strconv.Itoa(int(c.Port))))
	}
	if len(addrs) == 0 {
		addrs = []string{net.JoinHostPort(ep.Host, strconv.Itoa(int(ep.Port)))}
	}
	return &tunnelwirePeer{
		session: si,
		addrs:   addrs,
		tlsConf: &tls.Config{
			RootCAs:      pool,
			ServerName:   ep.ServerName,
			Certificates: []tls.Certificate{cert},
			NextProtos:   []string{alpn},
		},
	}
}

// dial connects to the first candidate address that completes a handshake.
func (p *tunnelwirePeer) dial(ctx context.Context) (*quicgo.Conn, error) {
	var firstErr error
	for _, addr := range p.addrs {
		dctx, cancel := context.WithTimeout(ctx, tunnelwireDialTimeout)
		conn, err := quicgo.DialAddr(dctx, addr, p.tlsConf, &quicgo.Config{
			MaxIdleTimeout:  time.Minute,
			KeepAlivePeriod: 5 * time.Second,
		})
		cancel()
		if err == nil {
			return conn, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, firstErr
}

// tunnelwireFrame returns the wire form of one tunnel message on a QUIC stream.
func tunnelwireFrame(m *manager.TunnelMessage) []byte {
	b, _ := proto.Marshal(m)
	f := binary.BigEndian.AppendUint32(nil, uint32(len(b)))
	return append(f, b...)
}

// tunnelwireStreamClosed reads from s until it fails or the read deadline passes. It
// returns true when the peer closed the stream, false when the read timed out or
// returned data.
func tunnelwireStreamClosed(s *quicgo.Stream, wait time.Duration) bool {
	_ = s.SetReadDeadline(time.Now().Add(wait))
	buf := make([]byte, 64)
	_, err := s.Read(buf)
	var se *quicgo.StreamError
	return errors.As(err, &se)
}

// tunnelwireManagerStates returns the traffic-manager pods' restart counts and readiness.
func tunnelwireManagerStates(t testing.TB, ctx context.Context, r *rt.Runtime) map[string]rt.TunnelwireContainerState {
	t.Helper()
	st, err := rt.TunnelwireContainerStates(ctx, r, managers.ManagerNamespace, tunnelwireManagerSel, tunnelwireManagerCtr)
	if err != nil || len(st) == 0 {
		t.Fatalf("traffic-manager pod state: %v (%d pods)", err, len(st))
	}
	return st
}

// tunnelwireAwaitManagerReady registers a cleanup that waits for the traffic-manager to
// be Ready again, so that an attack that crashes it does not break later tests.
func tunnelwireAwaitManagerReady(t testing.TB, ctx context.Context, r *rt.Runtime) {
	t.Helper()
	t.Cleanup(func() {
		deadline := time.Now().Add(tunnelwireReadyWait)
		for time.Now().Before(deadline) {
			st, err := rt.TunnelwireContainerStates(ctx, r, managers.ManagerNamespace, tunnelwireManagerSel, tunnelwireManagerCtr)
			ready := err == nil && len(st) > 0
			for _, s := range st {
				ready = ready && s.Ready == "True"
			}
			if ready {
				return
			}
			time.Sleep(quicPollInterval)
		}
		t.Errorf("the traffic-manager never became Ready again")
	})
}
