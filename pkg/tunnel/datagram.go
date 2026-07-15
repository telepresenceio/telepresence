package tunnel

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/quic-go/quic-go"

	"github.com/telepresenceio/clog"
)

// This file implements RFC 9221 unreliable datagram carriage for the Normal messages
// of a UDP flow: a QUIC connection that negotiated datagram support (quic.Config's
// EnableDatagrams on both ends) can carry a UDP payload as a DATAGRAM frame instead of
// on the flow's own stream, at the cost of the transport's ordering and delivery
// guarantees for that one message. There is no version byte in the wire format --
// support is negotiated by QUIC itself, via conn.ConnectionState().SupportsDatagrams,
// so a peer that never enabled it simply never receives one and the stream carries
// every message exactly as before.
//
// A datagram carries no teardown semantics of its own: streamInfo, DialOK, DialReject,
// Disconnect, KeepAlive, and closeSend all stay on the flow's stream, which is what
// still governs when the flow starts and ends. A datagram that arrives for a ConnID
// with no currently-registered flow -- most often a datagram racing the stream's own
// teardown -- is dropped without error, which is ordinary UDP semantics.

// EncodeDatagram frames one UDP payload for QUIC datagram carriage: a uvarint length
// prefix, the ConnID bytes it names, then the payload verbatim.
func EncodeDatagram(id ConnID, payload []byte) []byte {
	idb := []byte(id)
	buf := make([]byte, binary.MaxVarintLen64+len(idb)+len(payload))
	n := binary.PutUvarint(buf, uint64(len(idb)))
	n += copy(buf[n:], idb)
	n += copy(buf[n:], payload)
	return buf[:n]
}

// DecodeDatagram reverses EncodeDatagram. It reports an error if b is too short for
// its own length prefix, or shorter than the ConnID length the prefix declares.
func DecodeDatagram(b []byte) (ConnID, []byte, error) {
	idLen, n := binary.Uvarint(b)
	if n <= 0 {
		return "", nil, errors.New("truncated datagram: bad ConnID length prefix")
	}
	b = b[n:]
	if uint64(len(b)) < idLen {
		return "", nil, errors.New("truncated datagram: ConnID shorter than declared")
	}
	return ConnID(b[:idLen]), b[idLen:], nil
}

// datagramFlowBufferDepth is the capacity of each flow's inbound datagram delivery
// channel. Overflow is dropped (counted as dropped-full) rather than blocking, since
// blocking would re-impose the head-of-line stall datagrams exist to avoid. Enlarging
// this was investigated as the cause of the datagram-carriage latency penalty that
// the datagram-carriage experiment measured even at 0% loss (see perf/README.md): at depth 1024 the
// dropped-full counter stayed 0 while the penalty was unchanged, so the penalty is NOT
// overflow-driven and the modest depth is kept.
const datagramFlowBufferDepth = 8

// DatagramCapable is implemented by the GRPCStream/GRPCClientStream backing a QUIC
// tunnel stream (see quicStream in quic.go) so that pkg/tunnel's transport-agnostic
// stream type can reach the *quic.Conn carrying it without every Stream implementation
// (gRPC, local pipes) needing to know about datagrams at all.
type DatagramCapable interface {
	// SupportsDatagrams reports whether both ends of the connection negotiated RFC
	// 9221 datagram support.
	SupportsDatagrams() bool
	// SendDatagram sends payload as an unreliable QUIC datagram on the connection.
	SendDatagram(payload []byte) error
	// Conn returns the QUIC connection carrying this stream.
	Conn() *quic.Conn
}

// DatagramCounters accumulates observability totals for RFC 9221 datagram carriage of
// tunneled UDP payloads. Sent/Received count Normal messages that actually rode a
// datagram; FallbackToStream counts a Normal message of a datagram-capable UDP flow
// that was sent on the stream anyway (oversized, or a transient send error -- there is
// no latching, the next message tries the datagram path again); UnknownConn counts a
// received datagram whose ConnID has no currently-registered flow.
//
// A single instance may be shared across every QUIC connection a process ever routes
// datagrams for (the manager does this, to log one running total instead of one per
// client) or scoped to a single session's connection(s) across reprobes (the client
// does this, to log one total at session end); StartDatagramReceiver takes whichever
// instance the caller wants attributed.
type DatagramCounters struct {
	sent        atomic.Uint64
	received    atomic.Uint64
	fallback    atomic.Uint64
	unknownConn atomic.Uint64
	droppedFull atomic.Uint64
}

// Snapshot returns the current totals.
func (c *DatagramCounters) Snapshot() (sent, received, fallback, unknownConn, droppedFull uint64) {
	return c.sent.Load(), c.received.Load(), c.fallback.Load(), c.unknownConn.Load(), c.droppedFull.Load()
}

func (c *DatagramCounters) String() string {
	sent, received, fallback, unknownConn, droppedFull := c.Snapshot()
	return fmt.Sprintf("sent %d, received %d, fallback-to-stream %d, unknown-conn %d, dropped-full %d",
		sent, received, fallback, unknownConn, droppedFull)
}

// datagramConn is the per-QUIC-connection state StartDatagramReceiver installs: routes
// maps a ConnID to the chan []byte AttachDatagramRoute registered for it, and counters
// is whatever *DatagramCounters the caller of StartDatagramReceiver chose to attribute
// this connection's traffic to.
type datagramConn struct {
	routes   sync.Map // ConnID -> chan []byte
	counters *DatagramCounters
}

// datagramConns associates every QUIC connection currently routing datagrams with its
// datagramConn. Entries are added by StartDatagramReceiver and removed by its own
// receive loop once conn stops yielding datagrams. It is process-wide rather than
// threaded through every call site because a *quic.Conn is process-unique and
// AttachDatagramRoute has no other way to reach the receiver its connection's
// StartDatagramReceiver call installed.
var datagramConns sync.Map //nolint:gochecknoglobals // keyed by *quic.Conn identity, not app state

// StartDatagramReceiver enables inbound RFC 9221 datagram delivery for conn: it
// installs a routing table for conn's ConnIDs and starts the goroutine that decodes
// every DATAGRAM frame conn receives and dispatches it to whichever flow last called
// AttachDatagramRoute for that ConnID, counting the outcome in counters. The goroutine
// -- and conn's routing table with it -- exits when ctx is done or conn stops yielding
// datagrams, which is always the case once conn is closed. A nil conn (as tests that
// exercise the surrounding transport-selection logic without a real dial may pass) is
// a no-op: there is nothing to receive from.
func StartDatagramReceiver(ctx context.Context, conn *quic.Conn, counters *DatagramCounters) {
	if conn == nil {
		return
	}
	dc := &datagramConn{counters: counters}
	datagramConns.Store(conn, dc)
	go func() {
		defer datagramConns.Delete(conn)
		for {
			b, err := conn.ReceiveDatagram(ctx)
			if err != nil {
				return
			}
			id, payload, err := DecodeDatagram(b)
			if err != nil {
				clog.Tracef(ctx, "quic tunnel: dropping malformed datagram: %v", err)
				continue
			}
			counters.received.Add(1)
			v, ok := dc.routes.Load(id)
			if !ok {
				counters.unknownConn.Add(1)
				continue
			}
			select {
			case v.(chan []byte) <- payload:
			default:
				// The per-flow delivery channel is full: the consumer (the flow's
				// Receive loop) is not draining as fast as datagrams arrive in a
				// burst. Raw UDP tolerates the drop, but when the inner protocol is
				// itself reliable (QUIC/HTTP-3) a drop here is application-level loss
				// that the inner protocol must retransmit -- so this is counted, not
				// silent, and datagramFlowBufferDepth is sized to absorb a burst.
				counters.droppedFull.Add(1)
			}
		}
	}()
}

// AttachDatagramRoute registers s, a UDP-flavored Stream, for inbound datagram
// delivery keyed by its ConnID, and arranges for its Send to try the datagram fast
// path for Normal messages. It is a no-op -- including the returned detach func --
// unless s is backed by a DatagramCapable transport whose connection is both
// datagram-enabled and currently tracked by StartDatagramReceiver; that covers every
// flow whose transport didn't negotiate datagrams (an older peer, or the agent path,
// which never enables them) without the caller needing to check first. Call detach
// when the flow ends; it is safe to call more than once.
func AttachDatagramRoute(s Stream) (detach func()) {
	noop := func() {}

	var backing *stream
	switch v := s.(type) {
	case *clientStream:
		backing = &v.stream
	case *stream:
		backing = v
	default:
		return noop
	}

	dc, ok := backing.grpcStream.(DatagramCapable)
	if !ok || !dc.SupportsDatagrams() {
		return noop
	}
	v, ok := datagramConns.Load(dc.Conn())
	if !ok {
		return noop
	}
	entry := v.(*datagramConn)

	ch := make(chan []byte, datagramFlowBufferDepth)
	entry.routes.Store(backing.id, ch)
	backing.datagrams = ch
	backing.datagramCounters = entry.counters
	return func() {
		entry.routes.CompareAndDelete(backing.id, ch)
	}
}
