package usg

import (
	"context"

	usgrpc "github.com/telepresenceio/telepresence/rpc/v2/usg"
)

// MaxQueueSize is the FIFO buffer cap on both the client and the manager.
// On overflow the oldest entries are evicted so the most recent reports are
// always retained.
const MaxQueueSize = 10

// Per-report size budgets enforced at the sender. A collector must accept any
// report that conforms to these limits; the sender truncates envelope fields
// and silently discards excess entries so a stray oversize value never gets
// the whole batch rejected.
const (
	MaxBatchSize         = 200
	MaxInstallationIDLen = 255
	MaxSourceLen         = 32
	MaxTopicLen          = 128
	MaxVersionLen        = 64
	MaxOSLen             = 32
	MaxArchLen           = 32
	MaxEntryCount        = 32
	MaxEntryKeyLen       = 64
	MaxEntryValueLen     = 512
)

// Sink is the FIFO buffer holding reports between production and transmission.
// Implementations must be safe for concurrent use and must never block the
// caller of Enqueue. On overflow, Enqueue evicts the oldest entries so the
// newly enqueued report is always retained.
type Sink interface {
	// Enqueue adds a report to the FIFO. The implementation owns r after this
	// call and may marshal, copy, or persist it. Never blocks; on overflow it
	// evicts the oldest entries to make room.
	Enqueue(r *usgrpc.UsageReport)

	// Drain removes up to n oldest reports from the FIFO. The caller takes
	// ownership; if the caller fails to transmit them, it should Enqueue them
	// again (and may itself displace newer entries if the FIFO has filled up
	// in the meantime).
	Drain(n int) []*usgrpc.UsageReport

	// Len returns the current number of buffered reports. Used by tests; the
	// runtime path does not depend on it.
	Len() int
}

// producer holds the per-process configuration used to construct reports and
// is stored on the context. A producer's existence on the context means
// reporting is enabled — there is no "disabled producer". When reporting is
// off, no producer is attached, and producerFromContext returns nil.
type producer struct {
	source         Source
	installationID string
	version        string
	sink           Sink
}

type producerCtxKey struct{}

// WithProducer attaches a producer to ctx. Public so the bootstrap code in
// the client and traffic-manager can install a producer at the appropriate
// place in their context tree.
func WithProducer(ctx context.Context, p *producer) context.Context {
	if p == nil {
		return ctx
	}
	return context.WithValue(ctx, producerCtxKey{}, p)
}

func producerFromContext(ctx context.Context) *producer {
	if ctx == nil {
		return nil
	}
	p, _ := ctx.Value(producerCtxKey{}).(*producer)
	return p
}

// newProducer constructs a producer. sink is required and may not be nil —
// callers must check the enabled flag and decide not to call this if disabled.
func newProducer(source Source, installationID, version string, sink Sink) *producer {
	return &producer{
		source:         source,
		installationID: installationID,
		version:        version,
		sink:           sink,
	}
}
