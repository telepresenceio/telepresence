// Package kafka implements engine.Engine for one logical queue backed by
// Kafka, using github.com/twmb/franz-go. One Engine owns one activation: it
// pumps a source topic to per-route shadow topics through a splitter
// consumer group, fencing a predecessor process with static group
// membership and a transactional producer epoch.
package kafka

import (
	"context"
	"crypto/tls"
	"sync"
	"sync/atomic"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/queueagent/engine"
	"github.com/telepresenceio/telepresence/v2/pkg/queuestate"
)

// Config is the construction-time configuration for one Kafka Engine: the
// logical queue's identity, its source topic and original consumer group,
// and the broker connection this activation uses.
type Config struct {
	InstallID    string
	WorkloadUID  string
	QueueName    string
	ActivationID string

	// Brokers is the seed broker address list.
	Brokers []string

	// Source is the application's original source topic name.
	Source string

	// Group is the application's original consumer group name on Source.
	Group string

	// OffsetReset is "earliest" or "latest": where the splitter group
	// starts a partition with no committed offset in Group.
	OffsetReset string

	// TLS is the broker TLS configuration, or nil for a plaintext broker.
	TLS *tls.Config

	// SASLMechanism selects SASL authentication: "", "PLAIN",
	// "SCRAM-SHA-256", or "SCRAM-SHA-512".
	SASLMechanism string
	SASLUser      string
	SASLPassword  string

	// SourceEnvName and GroupEnvName are the application environment
	// variable names Prepare's EnvOverrides keys its values under.
	SourceEnvName string
	GroupEnvName  string
}

// routeEntry is one installed route in the pump's classification table.
type routeEntry struct {
	ID     string
	Filter map[string]string
	Shadow string
}

// Engine owns one logical Kafka queue for one activation.
type Engine struct {
	cfg Config

	appShadow     string
	appGroup      string
	splitterGroup string
	instanceID    string

	// mu guards client construction/teardown, the pump and ownership-monitor
	// goroutines' lifecycle fields, closed, and status. It is not held for
	// the duration of a broker round trip.
	mu            sync.Mutex
	admin         *kadm.Client
	producer      *kgo.Client
	consumer      *kgo.Client
	pumpCancel    func()
	pumpDone      chan struct{}
	monitorCancel func()
	monitorDone   chan struct{}
	closed        bool
	status        engine.Status

	// stopRequested is set before an orderly Stop cancels the pump's poll
	// context, so the pump can tell that cycle a cancellation causes apart
	// from a real failure. It is engine-wide, not per-run, since only one
	// pump run is ever active at a time.
	stopRequested atomic.Bool

	// started, stopped, and aborted record the activation's persisted
	// lifecycle phase, for VerifyCleanupReady and Cleanup to key their
	// enforcement on.
	started atomic.Bool
	stopped atomic.Bool
	aborted atomic.Bool

	// producerMu serializes every begin/produce/end-transaction cycle
	// across the pump and DrainRoute, and doubles as DrainRoute's publish
	// barrier: after removing a route from the table, acquiring and
	// releasing producerMu proves no in-flight produce still targets that
	// route's shadow.
	producerMu sync.Mutex

	// appShadowFinalMu guards appShadowFinal.
	appShadowFinalMu sync.Mutex

	// appShadowFinal caches, per partition, the app shadow's last real
	// (non-marker) record offset plus one, computed once DrainApplication
	// first needs it. It is immutable once populated: Stop has already
	// halted every write to the app shadow by the time DrainApplication
	// runs.
	appShadowFinal map[int32]int64

	// routes is the pump's route table, installed by ReconcileRoutes and
	// Recover and read once per record. An empty (never Store'd) value
	// classifies every record to the app shadow.
	routes atomic.Pointer[[]routeEntry]
}

// New constructs an Engine for cfg. It does not contact the broker; the
// first call to Prepare, Start, or Recover does so.
func New(cfg Config) *Engine {
	e := &Engine{
		cfg:           cfg,
		appShadow:     queuestate.AppShadowName(cfg.InstallID, cfg.WorkloadUID, cfg.QueueName, cfg.ActivationID),
		appGroup:      queuestate.AppGroupName(cfg.InstallID, cfg.WorkloadUID, cfg.QueueName, cfg.ActivationID),
		splitterGroup: queuestate.SplitterGroupName(cfg.InstallID, cfg.WorkloadUID, cfg.QueueName, cfg.ActivationID),
		status:        engine.Status{Healthy: true},
	}
	e.instanceID = e.splitterGroup
	empty := []routeEntry{}
	e.routes.Store(&empty)
	return e
}

// Status reports the engine's current health.
func (e *Engine) Status(_ context.Context) engine.Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.status
}

// Close releases held clients and connections. It deletes nothing.
func (e *Engine) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	pumpCancel := e.pumpCancel
	pumpDone := e.pumpDone
	monCancel := e.monitorCancel
	monDone := e.monitorDone
	e.pumpCancel = nil
	e.monitorCancel = nil
	e.mu.Unlock()

	if monCancel != nil {
		monCancel()
	}
	if pumpCancel != nil {
		pumpCancel()
	}
	if pumpDone != nil {
		<-pumpDone
	}
	if monDone != nil {
		<-monDone
	}

	e.mu.Lock()
	e.closeClientsLocked()
	e.mu.Unlock()
	return nil
}

// setUnhealthy records err as the reason the engine can no longer make
// progress and closes every broker client so a fenced process cannot keep
// acting as if it still owned the activation.
func (e *Engine) setUnhealthy(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.status.Healthy {
		return
	}
	e.status = engine.Status{Healthy: false, Detail: err.Error()}
	e.closed = true
	e.pumpCancel = nil
	e.closeClientsLocked()
}

// closeClientsLocked closes every held broker client. The caller must hold
// mu.
func (e *Engine) closeClientsLocked() {
	if e.consumer != nil {
		e.consumer.Close()
		e.consumer = nil
	}
	if e.producer != nil {
		e.producer.Close()
		e.producer = nil
	}
	if e.admin != nil {
		e.admin.Close()
		e.admin = nil
	}
}

// matches reports whether headers satisfies filter's equality conjunction.
// An empty filter matches every message.
func matches(filter, headers map[string]string) bool {
	for k, v := range filter {
		if headers[k] != v {
			return false
		}
	}
	return true
}

// headerMap converts Kafka record headers to a map, last-wins on a
// duplicate key.
func headerMap(hs []kgo.RecordHeader) map[string]string {
	m := make(map[string]string, len(hs))
	for _, h := range hs {
		m[h.Key] = string(h.Value)
	}
	return m
}

// toKgoHeaders converts a header map to Kafka record headers.
func toKgoHeaders(headers map[string]string) []kgo.RecordHeader {
	hs := make([]kgo.RecordHeader, 0, len(headers))
	for k, v := range headers {
		hs = append(hs, kgo.RecordHeader{Key: k, Value: []byte(v)})
	}
	return hs
}
