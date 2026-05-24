package usg

import (
	"sync"

	usgrpc "github.com/telepresenceio/telepresence/rpc/v2/usg"
)

// MemSink is the in-memory FIFO used by the traffic-manager. It holds at most
// MaxQueueSize reports; on overflow the oldest entries are evicted so the most
// recent are always retained.
type MemSink struct {
	mu  sync.Mutex
	buf []*usgrpc.UsageReport
	cap int
}

// NewMemSink returns a FIFO with the package's standard cap.
func NewMemSink() *MemSink {
	return &MemSink{cap: MaxQueueSize}
}

func (m *MemSink) Enqueue(r *usgrpc.UsageReport) {
	if r == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.buf) >= m.cap {
		// Evict the oldest entry so the most recent reports always win.
		drop := len(m.buf) - m.cap + 1
		m.buf = m.buf[drop:]
	}
	m.buf = append(m.buf, r)
}

func (m *MemSink) Drain(n int) []*usgrpc.UsageReport {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.buf) == 0 {
		return nil
	}
	if n <= 0 || n > len(m.buf) {
		n = len(m.buf)
	}
	out := make([]*usgrpc.UsageReport, n)
	copy(out, m.buf[:n])
	m.buf = m.buf[n:]
	return out
}

func (m *MemSink) Len() int {
	m.mu.Lock()
	n := len(m.buf)
	m.mu.Unlock()
	return n
}
