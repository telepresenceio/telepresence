package quicforwarder

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/qlog"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/quicfwd"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

// These benchmarks decompose the transport stack to locate where bulk-transfer
// throughput goes, after a field measurement showed the QUIC path well behind
// the port-forwarded gRPC path at zero packet loss:
//
//	tcp-direct     kernel TCP loopback: the reference ceiling.
//	quic-direct    client <-> quic-go server, no forwarder: userspace QUIC cost.
//	quic-forwarded the same, through the forwarder: adds the packet-relay cost.
//
// The payload server implements one trivial protocol: the client opens a
// stream, sends an 8-byte big-endian byte count, and the server writes that
// many bytes and closes. Run with -bench Throughput -benchtime and optionally
// -cpuprofile to see where the time goes.
const benchPayload = 8 * 1024 * 1024

func benchTLSConfig(tb testing.TB) *tls.Config {
	tb.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		tb.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "bench"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{quicfwd.ManagerSNI},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		tb.Fatal(err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		NextProtos:   []string{tunnel.QuicALPN},
	}
}

// servePayloadStream answers the benchmark protocol on one bidirectional stream.
func servePayloadStream(s io.ReadWriteCloser) {
	defer s.Close()
	var szb [8]byte
	if _, err := io.ReadFull(s, szb[:]); err != nil {
		return
	}
	n := binary.BigEndian.Uint64(szb[:])
	chunk := make([]byte, 64*1024)
	for n > 0 {
		c := uint64(len(chunk))
		if c > n {
			c = n
		}
		if _, err := s.Write(chunk[:c]); err != nil {
			return
		}
		n -= c
	}
}

// startBenchQuicServer serves the payload protocol over QUIC on 127.0.0.1 with
// the production CID generator (as backends behind the forwarder run).
func startBenchQuicServer(tb testing.TB) (addr string, closer func()) {
	tb.Helper()
	ip := netip.MustParseAddr("127.0.0.1")
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: ip.AsSlice()})
	if err != nil {
		tb.Fatal(err)
	}
	tr := &quic.Transport{Conn: udp, ConnectionIDGenerator: quicfwd.NewCIDGenerator(ip)}
	// The qlog tracer is a no-op unless QLOGDIR is set; with it set, the server
	// (sender) side's congestion controller and loss recovery become inspectable.
	ln, err := tr.Listen(benchTLSConfig(tb), &quic.Config{MaxIdleTimeout: time.Minute, Tracer: qlog.DefaultConnectionTracer})
	if err != nil {
		tb.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			conn, err := ln.Accept(ctx)
			if err != nil {
				return
			}
			go func() {
				for {
					s, err := conn.AcceptStream(ctx)
					if err != nil {
						return
					}
					// CancelRead releases the receive direction, which
					// servePayloadStream never reads to EOF; without it the stream
					// never terminates, its stream-limit credit is never returned,
					// and the concurrent benchmarks hang once the limit is spent.
					go func() {
						servePayloadStream(s)
						s.CancelRead(0)
					}()
				}
			}()
		}
	}()
	return udp.LocalAddr().String(), func() { cancel(); _ = ln.Close(); _ = udp.Close(); _ = tr.Close() }
}

func startBenchTCPServer(tb testing.TB) (addr string, closer func()) {
	tb.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go servePayloadStream(c)
		}
	}()
	return ln.Addr().String(), func() { _ = ln.Close() }
}

// startBenchForwarder puts the production forwarder in front of backendAddr and
// returns the forwarder's dialable address and the *Forwarder itself, for tests that
// need to inspect its internal state (e.g. flow-table size).
func startBenchForwarder(tb testing.TB, backendAddr string) (addr string, fwd *Forwarder, closer func()) {
	tb.Helper()
	ap := netip.MustParseAddrPort(backendAddr)
	allowlist := NewAllowlist(0)
	allowlist.update(context.Background(),
		[]*rpc.QuicBackend{{Ip: ap.Addr().AsSlice(), Kind: "manager", Port: int32(ap.Port())}})
	fwd, err := Listen(&Env{ListenPort: 0}, allowlist)
	if err != nil {
		tb.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = fwd.Serve(ctx) }()
	return net.JoinHostPort("127.0.0.1", fmt.Sprint(fwd.front.LocalAddr().(*net.UDPAddr).Port)),
		fwd,
		func() { cancel(); _ = fwd.front.Close() }
}

func dialBenchQuic(tb testing.TB, addr string) *quic.Conn {
	tb.Helper()
	conn, err := quic.DialAddr(context.Background(), addr, &tls.Config{
		InsecureSkipVerify: true,
		ServerName:         quicfwd.ManagerSNI,
		NextProtos:         []string{tunnel.QuicALPN},
	}, &quic.Config{MaxIdleTimeout: time.Minute, KeepAlivePeriod: 15 * time.Second})
	if err != nil {
		tb.Fatal(err)
	}
	return conn
}

// fetchQuic downloads benchPayload bytes on a fresh stream of conn.
func fetchQuic(tb testing.TB, conn *quic.Conn) {
	tb.Helper()
	s, err := conn.OpenStreamSync(context.Background())
	if err != nil {
		tb.Fatal(err)
	}
	defer s.Close()
	var szb [8]byte
	binary.BigEndian.PutUint64(szb[:], benchPayload)
	if _, err := s.Write(szb[:]); err != nil {
		tb.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, s); err != nil {
		tb.Fatal(err)
	}
}

func fetchTCP(tb testing.TB, addr string) {
	tb.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		tb.Fatal(err)
	}
	defer c.Close()
	var szb [8]byte
	binary.BigEndian.PutUint64(szb[:], benchPayload)
	if _, err := c.Write(szb[:]); err != nil {
		tb.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, c); err != nil {
		tb.Fatal(err)
	}
}

func BenchmarkThroughputTCPDirect(b *testing.B) {
	addr, closeSrv := startBenchTCPServer(b)
	defer closeSrv()
	b.SetBytes(benchPayload)
	b.ResetTimer()
	for range b.N {
		fetchTCP(b, addr)
	}
}

func BenchmarkThroughputQuicDirect(b *testing.B) {
	addr, closeSrv := startBenchQuicServer(b)
	defer closeSrv()
	conn := dialBenchQuic(b, addr)
	defer func() { _ = conn.CloseWithError(0, "") }()
	b.SetBytes(benchPayload)
	b.ResetTimer()
	for range b.N {
		fetchQuic(b, conn)
	}
}

func BenchmarkThroughputQuicForwarded(b *testing.B) {
	backendAddr, closeSrv := startBenchQuicServer(b)
	defer closeSrv()
	fwdAddr, _, closeFwd := startBenchForwarder(b, backendAddr)
	defer closeFwd()
	conn := dialBenchQuic(b, fwdAddr)
	defer func() { _ = conn.CloseWithError(0, "") }()
	b.SetBytes(benchPayload)
	b.ResetTimer()
	for range b.N {
		fetchQuic(b, conn)
	}
}

// The Concurrent variants mirror the field experiment's shape: 50 streams share
// one connection (QUIC) or use one TCP connection each, all active at once.
func BenchmarkThroughputQuicForwardedConcurrent50(b *testing.B) {
	backendAddr, closeSrv := startBenchQuicServer(b)
	defer closeSrv()
	fwdAddr, _, closeFwd := startBenchForwarder(b, backendAddr)
	defer closeFwd()
	conn := dialBenchQuic(b, fwdAddr)
	defer func() { _ = conn.CloseWithError(0, "") }()
	b.SetBytes(50 * benchPayload)
	b.ResetTimer()
	for range b.N {
		var wg sync.WaitGroup
		wg.Add(50)
		for range 50 {
			go func() {
				defer wg.Done()
				fetchQuic(b, conn)
			}()
		}
		wg.Wait()
	}
}

func BenchmarkThroughputTCPConcurrent50(b *testing.B) {
	addr, closeSrv := startBenchTCPServer(b)
	defer closeSrv()
	b.SetBytes(50 * benchPayload)
	b.ResetTimer()
	for range b.N {
		var wg sync.WaitGroup
		wg.Add(50)
		for range 50 {
			go func() {
				defer wg.Done()
				fetchTCP(b, addr)
			}()
		}
		wg.Wait()
	}
}
