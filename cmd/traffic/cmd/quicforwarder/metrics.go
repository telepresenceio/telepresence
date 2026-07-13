package quicforwarder

import (
	"context"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/telepresenceio/clog"
)

// dropReason categorizes why a datagram was silently dropped, per "The forwarder"
// section of docs/plans/quic-transport/design.md: anything that resolves to no
// backend is dropped silently, with a rate-limited metric and no response.
type dropReason int

const (
	// dropNotReady means the backend allowlist hasn't received its first
	// snapshot yet, so nothing can be validated and everything is dropped.
	dropNotReady dropReason = iota
	// dropGarbage means the datagram didn't parse as QUIC packet(s) at all, or
	// an Initial packet's own contents were malformed once decrypted.
	dropGarbage
	// dropUnknownVersion means a long-header packet's QUIC version is either
	// the version-negotiation marker or one this package cannot read Initial
	// keys for.
	dropUnknownVersion
	// dropAllowlistMiss means a connection ID decoded to a pod IP that isn't a
	// live, allowlisted backend.
	dropAllowlistMiss
	// dropUnresolvedSNI means a ClientHello's SNI didn't resolve to any
	// allowlisted backend: an unrecognized name, an agent SNI (unsupported
	// until phase 6 adds agent backends), or a manager SNI with no manager
	// currently allowlisted.
	dropUnresolvedSNI
	// dropCapExceeded means a handshake-cache entry's buffered datagram
	// count or byte cap was exceeded before the SNI resolved.
	dropCapExceeded
	// dropTTLExpired means a handshake-cache entry aged out (handshakeTTL)
	// without resolving.
	dropTTLExpired
	// numDropReasons is a sentinel, not a real reason.
	numDropReasons
)

func (d dropReason) String() string {
	switch d {
	case dropNotReady:
		return "allowlist-not-ready"
	case dropGarbage:
		return "garbage"
	case dropUnknownVersion:
		return "unknown-version"
	case dropAllowlistMiss:
		return "allowlist-miss"
	case dropUnresolvedSNI:
		return "unresolved-sni"
	case dropCapExceeded:
		return "handshake-cap-exceeded"
	case dropTTLExpired:
		return "handshake-ttl-expired"
	default:
		return "unknown(" + strconv.Itoa(int(d)) + ")"
	}
}

// metrics accumulates the forwarder's forwarded/dropped-by-reason counters. All
// operations are safe for concurrent use. Prometheus export is deliberately out of
// scope for this task; LogSnapshot is how the counters surface, at debug level, until a
// later task wires them up as real metrics.
type metrics struct {
	forwarded atomic.Int64
	dropped   [numDropReasons]atomic.Int64
}

func newMetrics() *metrics {
	return &metrics{}
}

func (m *metrics) addForwarded(n int64) {
	m.forwarded.Add(n)
}

func (m *metrics) addDrop(reason dropReason) {
	m.dropped[reason].Add(1)
}

// LogSnapshot logs the current cumulative counters at debug level. It is intended to be
// called periodically (see forwarder.go's metricsLogInterval), not per packet.
func (m *metrics) LogSnapshot(ctx context.Context) {
	var b strings.Builder
	b.WriteString("forwarded=")
	b.WriteString(strconv.FormatInt(m.forwarded.Load(), 10))
	for r := dropReason(0); r < numDropReasons; r++ {
		if n := m.dropped[r].Load(); n > 0 {
			b.WriteString(" dropped[")
			b.WriteString(r.String())
			b.WriteString("]=")
			b.WriteString(strconv.FormatInt(n, 10))
		}
	}
	clog.Debugf(ctx, "quic-forwarder counters: %s", b.String())
}
