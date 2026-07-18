package quicforwarder

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/quicfwd"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

// natProxy is a pure-Go stand-in for a NAT gateway or a client's network interface: it
// relays UDP datagrams between a fixed client-facing socket (what the test's QUIC client
// dials) and the forwarder, through a forwarder-facing socket that flip replaces with a
// fresh one bound to a new ephemeral port. From the forwarder's point of view this is
// indistinguishable from a real NAT rebinding or a client roaming to a new interface:
// the same logical client suddenly sends from an unrecognized netip.AddrPort.
type natProxy struct {
	t       *testing.T
	client  *net.UDPConn
	fwdAddr *net.UDPAddr

	mu  sync.Mutex
	out *net.UDPConn

	clientAddr atomic.Pointer[net.UDPAddr]

	// flipAfterN, when non-zero, makes relayFromClient flip the proxy itself once it
	// has relayed exactly that many client->forwarder datagrams -- used to land a flip
	// at a precise point in the handshake instead of racing it with a sleep.
	flipAfterN atomic.Int64
	relayed    atomic.Int64
}

func newNatProxy(t *testing.T, fwdAddr string) *natProxy {
	t.Helper()
	client, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	require.NoError(t, err)
	fa, err := net.ResolveUDPAddr("udp", fwdAddr)
	require.NoError(t, err)
	out, err := net.DialUDP("udp", nil, fa)
	require.NoError(t, err)

	p := &natProxy{t: t, client: client, fwdAddr: fa, out: out}
	go p.relayFromClient()
	go p.relayFromForwarder(out)
	return p
}

// addr is the address the test's QUIC client dials.
func (p *natProxy) addr() string { return p.client.LocalAddr().String() }

func (p *natProxy) currentOut() *net.UDPConn {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.out
}

func (p *natProxy) relayFromClient() {
	buf := make([]byte, maxDatagramSize)
	for {
		n, addr, err := p.client.ReadFromUDP(buf)
		if err != nil {
			return
		}
		p.clientAddr.Store(addr)
		_, _ = p.currentOut().Write(buf[:n])
		if target := p.flipAfterN.Load(); target != 0 && p.relayed.Add(1) == target {
			p.flip(p.t)
		}
	}
}

// relayFromForwarder pumps datagrams arriving on one incarnation of the
// forwarder-facing socket back to the client. flip starts a fresh goroutine of this for
// each new socket; the previous one exits on its own once flip closes the socket it is
// blocked reading.
func (p *natProxy) relayFromForwarder(out *net.UDPConn) {
	buf := make([]byte, maxDatagramSize)
	for {
		n, err := out.Read(buf)
		if err != nil {
			return
		}
		if addr := p.clientAddr.Load(); addr != nil {
			_, _ = p.client.WriteToUDP(buf[:n], addr)
		}
	}
}

// flip closes the current forwarder-facing socket and replaces it with a fresh one
// dialed from a new ephemeral port: the forwarder's next datagram from this natProxy
// arrives from a source address it has never seen before, exactly like NAT rebinding
// or an interface roam.
func (p *natProxy) flip(t *testing.T) {
	t.Helper()
	newOut, err := net.DialUDP("udp", nil, p.fwdAddr)
	require.NoError(t, err)
	p.mu.Lock()
	old := p.out
	p.out = newOut
	p.mu.Unlock()
	_ = old.Close()
	go p.relayFromForwarder(newOut)
}

// flipAfterClientDatagrams arms an automatic flip that fires from inside
// relayFromClient right after the n-th client->forwarder datagram is relayed, before
// the next one (if any) is read. Must be called before the client sends anything.
func (p *natProxy) flipAfterClientDatagrams(n int) {
	p.flipAfterN.Store(int64(n))
}

func (p *natProxy) close() {
	_ = p.currentOut().Close()
	_ = p.client.Close()
}

// fetchQuicInParts downloads size bytes on a fresh stream of conn using the bench
// payload protocol (an 8-byte big-endian size prefix, servePayloadStream on the other
// end), split into numParts equal reads. Between each part it calls between with the
// 0-based index of the boundary just crossed, before continuing to read -- which lands
// a NAT flip at a precise, deterministic point in the transfer instead of racing it
// with a sleep.
func fetchQuicInParts(t *testing.T, conn *quic.Conn, size int64, numParts int, between func(boundary int)) {
	t.Helper()
	s, err := conn.OpenStreamSync(context.Background())
	require.NoError(t, err)
	defer s.Close()

	var szb [8]byte
	binary.BigEndian.PutUint64(szb[:], uint64(size))
	_, err = s.Write(szb[:])
	require.NoError(t, err)

	part := size / int64(numParts)
	for i := range numParts - 1 {
		_, err := io.CopyN(io.Discard, s, part)
		require.NoErrorf(t, err, "reading part %d of %d", i+1, numParts)
		between(i)
	}
	_, err = io.Copy(io.Discard, s)
	require.NoError(t, err)
}

// dialQuicWithConfig dials the bench protocol's ManagerSNI/ALPN with an explicit
// *quic.Config, for variants that need a short handshake or idle timeout rather than
// dialBenchQuic's throughput-oriented one-minute defaults.
func dialQuicWithConfig(t *testing.T, addr string, cfg *quic.Config) (*quic.Conn, error) {
	t.Helper()
	return quic.DialAddr(context.Background(), addr, &tls.Config{
		InsecureSkipVerify: true,
		ServerName:         quicfwd.ManagerSNI,
		NextProtos:         []string{tunnel.QuicALPN},
	}, cfg)
}

// TestMigration_NATRebind verifies connection migration: a NAT rebind (or
// client interface roam) mid-transfer must not reset the QUIC connection, because the
// forwarder's steady-state routing key is the server-issued connection ID
// (pkg/quicfwd's CID codec), never the client's source address. natProxy simulates the
// rebind by swapping its forwarder-facing socket -- and therefore the source address
// the forwarder sees -- for a fresh one mid-transfer; the forwarder's cold path
// (Router.Route case (b), routeByCID) picks the new source up from the unmodified
// server-issued CID on the very next short-header packet and creates a second flow to
// the same backend, exactly as it would for an unrelated new connection. The same
// cold-path routing is what already lets RFC 9221 DATAGRAM frames -- which ride
// ordinary short-header packets -- survive a forwarder restart; migration exercises no
// different code path, since the forwarder never inspects frame types.
func TestMigration_NATRebind(t *testing.T) {
	backendAddr, closeSrv := startBenchQuicServer(t)
	defer closeSrv()
	fwdAddr, fwd, closeFwd := startBenchForwarder(t, backendAddr)
	defer closeFwd()

	proxy := newNatProxy(t, fwdAddr)
	defer proxy.close()

	conn := dialBenchQuic(t, proxy.addr())
	defer func() { _ = conn.CloseWithError(0, "") }()

	// Establish the flow before rebinding: warm-up traffic pushes the connection
	// through the handshake so the CID the flip's cold path must route by is already
	// server-issued.
	fetchQuicInParts(t, conn, 64*1024, 1, nil)
	require.Equal(t, 1, fwd.flows.count(), "warm-up transfer must have created exactly one flow")

	fetchQuicInParts(t, conn, benchPayload, 2, func(int) { proxy.flip(t) })

	assert.Equal(t, 2, fwd.flows.count(),
		"the rebind must create a second flow-table entry for the new source, alongside the (now idle) original")
}

// TestMigration_NATRebindTwice repeats the rebind within a single transfer: two flips,
// three source addresses, one connection. Nothing in the forwarder's routing keys off
// how many times a source has already changed, so this is expected to behave exactly
// like a single flip, just with one more flow-table entry.
func TestMigration_NATRebindTwice(t *testing.T) {
	backendAddr, closeSrv := startBenchQuicServer(t)
	defer closeSrv()
	fwdAddr, fwd, closeFwd := startBenchForwarder(t, backendAddr)
	defer closeFwd()

	proxy := newNatProxy(t, fwdAddr)
	defer proxy.close()

	conn := dialBenchQuic(t, proxy.addr())
	defer func() { _ = conn.CloseWithError(0, "") }()

	fetchQuicInParts(t, conn, 64*1024, 1, nil)
	require.Equal(t, 1, fwd.flows.count())

	fetchQuicInParts(t, conn, 3*1024*1024, 3, func(int) { proxy.flip(t) })

	assert.Equal(t, 3, fwd.flows.count(), "two rebinds must leave three flow-table entries: the original plus one per flip")
}

// TestMigration_NATRebindThenIdlePastKeepAlive rebinds while the connection is
// otherwise idle, then waits past MaxIdleTimeout doing nothing but letting keep-alives
// fire. If keep-alives did not ride the new forwarder flow, the connection would time
// out during the sleep and the final exchange would fail.
func TestMigration_NATRebindThenIdlePastKeepAlive(t *testing.T) {
	backendAddr, closeSrv := startBenchQuicServer(t)
	defer closeSrv()
	fwdAddr, fwd, closeFwd := startBenchForwarder(t, backendAddr)
	defer closeFwd()

	proxy := newNatProxy(t, fwdAddr)
	defer proxy.close()

	const (
		idleTimeout = 600 * time.Millisecond
		keepAlive   = 150 * time.Millisecond
	)
	conn, err := dialQuicWithConfig(t, proxy.addr(), &quic.Config{MaxIdleTimeout: idleTimeout, KeepAlivePeriod: keepAlive})
	require.NoError(t, err)
	defer func() { _ = conn.CloseWithError(0, "") }()

	fetchQuicInParts(t, conn, 4096, 1, nil)
	require.Equal(t, 1, fwd.flows.count())

	proxy.flip(t)
	time.Sleep(3 * idleTimeout)

	fetchQuicInParts(t, conn, 4096, 1, nil)
	assert.Equal(t, 2, fwd.flows.count(),
		"the connection must still be alive after idling past MaxIdleTimeout on the rebound path: only keep-alives kept it up")
}

// TestMigration_NATRebindMidHandshakeFailsCleanly documents the one migration case that
// does not survive: a rebind that straddles a single connection attempt's own
// Initial-level retry.
//
// This client's ClientHello does not fit in one Initial packet, so it is sent as two
// Initial datagrams; routeInitial's handshake cache -- keyed on (source, client DCID) --
// reassembles them and resolves the SNI once both have arrived from the same source.
// flipAfterClientDatagrams(2) lets that first attempt fully resolve (case (b)/(d) CID
// routing is available from here on, same as the established-flow variants above), then
// tears down the pre-flip source before the forwarder's response reaches it. quic-go's
// client, having received no answer, retransmits the whole ClientHello; that
// retransmission arrives from the post-flip source and independently completes its own
// reassembly there. The forwarder ends up dialing the backend twice (asserted below via
// the flow count) for what the client considers one connection attempt, and a client
// with only one Dial() in flight cannot reconcile two independently-negotiated
// handshakes: the dial fails.
//
// A rebind whose retransmission lands cleanly under a single new source recovers the
// same way an established flow does -- this is not "any rebind during the handshake
// fails," only one that leaves two independently-resolved attempts alive at once. RFC
// 9000 §9 sidesteps exactly this by forbidding migration before the handshake is
// confirmed: there is no single connection yet for a mid-handshake source change to
// migrate.
func TestMigration_NATRebindMidHandshakeFailsCleanly(t *testing.T) {
	backendAddr, closeSrv := startBenchQuicServer(t)
	defer closeSrv()
	fwdAddr, fwd, closeFwd := startBenchForwarder(t, backendAddr)
	defer closeFwd()

	proxy := newNatProxy(t, fwdAddr)
	defer proxy.close()
	proxy.flipAfterClientDatagrams(2)

	_, err := dialQuicWithConfig(t, proxy.addr(), &quic.Config{HandshakeIdleTimeout: 2 * time.Second, MaxIdleTimeout: time.Minute})
	require.Error(t, err, "a rebind that straddles one connection attempt's own Initial retry is expected to fail the attempt, not migrate it")
	assert.Equal(t, 2, fwd.flows.count(),
		"the forwarder dialed the backend twice -- once per independently-resolved attempt -- which is exactly why the client, with only one Dial() in flight, cannot reconcile the result")
}
