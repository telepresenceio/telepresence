// Package rabbitmq implements engine.Engine for RabbitMQ, using
// github.com/rabbitmq/amqp091-go for the AMQP protocol and the RabbitMQ
// management HTTP API for queue introspection, consumer-count monitoring,
// and stale-connection eviction.
//
// One Engine owns one logical queue for one activation: a single source
// queue, a durable app shadow, and zero or more durable session shadows, one
// per developer route. There is no RabbitMQ consumer-group concept, so
// EnvOverrides from Prepare carries only the source env override.
package rabbitmq

import (
	"context"
	"crypto/tls"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/queueagent/engine"
	"github.com/telepresenceio/telepresence/v2/pkg/queuestate"
)

// Config is the construction-time identity and connection configuration for
// one Engine. Two Engine values built from Configs that agree on InstallID,
// WorkloadUID, QueueName, and ActivationID are the same logical owner, even
// across a process restart.
type Config struct {
	// InstallID, WorkloadUID, QueueName, and ActivationID identify this
	// activation. They are the inputs to every bounded resource name this
	// engine derives via pkg/queuestate.
	InstallID    string
	WorkloadUID  string
	QueueName    string
	ActivationID string

	// URL is the AMQP connection URL (amqp:// or amqps://). Its path
	// component is the vhost every management API call is scoped to
	// (default "/" when absent), matching how the AMQP connection itself
	// resolves it.
	URL string

	// ManagementURL is the RabbitMQ management HTTP API base URL. Basic-auth
	// credentials are read from its userinfo, if present.
	ManagementURL string

	// Source is the name of the source queue this activation splits.
	Source string

	// SourceEnvName is the application environment variable Prepare's
	// EnvOverrides points at the app shadow.
	SourceEnvName string

	// TLS configures the AMQP connection's TLS transport. Nil uses the
	// scheme in URL as-is (a bare amqp:// URL stays unencrypted).
	TLS *tls.Config
}

// Engine implements engine.Engine for one RabbitMQ logical queue.
type Engine struct {
	engine.Lifecycle

	cfg Config

	appShadowName string
	lockName      string

	mgmt *mgmtClient

	// conn is this activation's one connection: it holds the exclusive lock
	// queue, the source consumer, the shared publish channel, and every
	// admin declare/delete this engine issues. It is deliberately one
	// connection, not several: fencing a predecessor means force-closing its
	// connection through the management API (evictLockHolder), and that
	// must take its exclusive source consumer down with its lock queue, not
	// leave the consumer running on a surviving connection. Opened lazily
	// and reused across Prepare/Start/Recover.
	connMu sync.Mutex
	conn   *amqp.Connection

	// publishCh is the single channel every publish -- pump, route drain, and
	// handback -- goes through. publishMu serializes publishes on it to one
	// outstanding publish at a time, which is what makes correlating a
	// basic.return with its confirm by ordering (not by stamping message
	// content) correct: dispatch is single-threaded per connection, so any
	// return for a publish is fully delivered to its notify channel before
	// that publish's confirmation is.
	publishCh *amqp.Channel
	publishMu sync.Mutex
	confirms  chan amqp.Confirmation
	returns   chan amqp.Return

	sourceType string // "classic" or "quorum", discovered in Prepare

	// runMu guards every field describing the running pump: the source
	// consumer channel/tag and the pump/quorum-monitor goroutine handles.
	// They are started by Start/Recover and read by Stop, Close, and the
	// lock-connection watcher, any of which can run concurrently with the
	// goroutines they describe.
	runMu       sync.Mutex
	running     bool // true once a pump has been started by this instance; guards Start/Recover idempotency
	consumeCh   *amqp.Channel
	consumerTag string
	pumpCancel  context.CancelFunc
	pumpDone    chan struct{}
	monitorStop context.CancelFunc
	monitorDone chan struct{}

	// lockHeld records whether this instance currently holds the exclusive
	// lock queue on conn. lockWatchWG is joined by Close so it waits for the
	// fencing watcher goroutine to exit.
	lockMu      sync.Mutex
	lockHeld    bool
	lockWatchWG sync.WaitGroup

	routesMu sync.Mutex
	routes   []engine.Route // ordered: classification uses first match

	// retainedShadows records quorum session shadow names DrainRoute or
	// Cleanup verified empty and left in place (no loss-proof delete exists
	// for a quorum queue), guarded by routesMu, so Cleanup can still find and
	// report them once a route is no longer in routes.
	retainedShadows map[string]bool

	statusMu sync.Mutex
	healthy  bool
	detail   string
}

// New constructs an Engine for cfg. It performs no I/O; connections are
// opened lazily by Prepare, Start, and Recover.
func New(cfg Config) *Engine {
	return &Engine{
		cfg:             cfg,
		appShadowName:   queuestate.AppShadowName(cfg.InstallID, cfg.WorkloadUID, cfg.QueueName, cfg.ActivationID),
		lockName:        queuestate.LockQueueName(cfg.InstallID, cfg.WorkloadUID, cfg.QueueName, cfg.ActivationID),
		mgmt:            newMgmtClient(cfg.ManagementURL, vhostOf(cfg.URL)),
		healthy:         true,
		retainedShadows: make(map[string]bool),
	}
}

// vhostOf returns the vhost component of an AMQP URL, defaulting to "/" the
// same way the broker does. A parse failure here is not reported: the same
// URL fails loudly again, for real, the first time dialConn parses it.
func vhostOf(rawURL string) string {
	uri, err := amqp.ParseURI(rawURL)
	if err != nil {
		return "/"
	}
	return uri.Vhost
}

// Status reports the engine's cached health. It never performs I/O.
func (e *Engine) Status(context.Context) engine.Status {
	e.statusMu.Lock()
	defer e.statusMu.Unlock()
	return engine.Status{Healthy: e.healthy, Detail: e.detail}
}

// setUnhealthy records detail as the reason this engine can no longer make
// progress. Once set, health never recovers within one instance's lifetime --
// a fenced or otherwise broken engine is replaced, not repaired in place.
func (e *Engine) setUnhealthy(detail string) {
	e.statusMu.Lock()
	defer e.statusMu.Unlock()
	if !e.healthy {
		return
	}
	e.healthy = false
	e.detail = detail
}

// healthErr returns a non-nil error, describing why, when the engine is
// fenced or otherwise broken. Every exported method that requires the fence
// checks this first, per the interface contract: once a newer instance has
// taken the fence, every later operation on this instance must fail.
func (e *Engine) healthErr() error {
	e.statusMu.Lock()
	defer e.statusMu.Unlock()
	if e.healthy {
		return nil
	}
	return fmt.Errorf("rabbitmq engine for queue %q is unhealthy: %s", e.cfg.QueueName, e.detail)
}

// Close releases held clients and connections. It deletes nothing: the lock
// queue, if still held, disappears as a side effect of closing conn (it is
// exclusive and auto-delete), which is the intended fence release for a
// process that exits without an orderly Cleanup.
func (e *Engine) Close() error {
	e.stopRunning()

	e.connMu.Lock()
	conn := e.conn
	e.conn = nil
	e.connMu.Unlock()

	var err error
	if conn != nil && !conn.IsClosed() {
		err = conn.Close()
	}
	e.lockWatchWG.Wait()

	return err
}

// setRun records the source consumer and the pump/quorum-monitor lifecycle
// handles started by Start/Recover.
func (e *Engine) setRun(
	consumeCh *amqp.Channel, consumerTag string,
	pumpCancel context.CancelFunc, pumpDone chan struct{},
	monitorStop context.CancelFunc, monitorDone chan struct{},
) {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	e.consumeCh = consumeCh
	e.consumerTag = consumerTag
	e.pumpCancel = pumpCancel
	e.pumpDone = pumpDone
	e.monitorStop = monitorStop
	e.monitorDone = monitorDone
}

// consumer returns the source consumer channel and tag, if a pump is
// running.
func (e *Engine) consumer() (*amqp.Channel, string) {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	return e.consumeCh, e.consumerTag
}

// pumpDoneChan returns the channel that closes when the pump goroutine
// exits, if one is running.
func (e *Engine) pumpDoneChan() chan struct{} {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	return e.pumpDone
}

// cancelPump cancels the pump loop's context, if a pump is running. It is
// safe to call from the lock-connection watcher concurrently with Start
// still assigning the handle: a cancellation that arrives just before
// setRun is a narrow, harmless race -- the pump loop checks ctx.Err() and
// e.healthErr() on every iteration, so a fenced engine that briefly misses
// the initial cancel signal still stops at its next iteration.
func (e *Engine) cancelPump() {
	e.runMu.Lock()
	cancel := e.pumpCancel
	e.runMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// stopMonitor cancels and joins the quorum-source monitor, if one is
// running. An orderly Stop or Abort removes the engine's own source
// consumer, making a subsequent zero-consumer reading expected rather than
// an ownership violation, so the monitor must end before the consumer does.
func (e *Engine) stopMonitor() {
	e.runMu.Lock()
	monitorStop, monitorDone := e.monitorStop, e.monitorDone
	e.monitorStop = nil
	e.monitorDone = nil
	e.runMu.Unlock()

	if monitorStop != nil {
		monitorStop()
	}
	if monitorDone != nil {
		<-monitorDone
	}
}

// stopSourceConsumer stops the quorum monitor, cancels the source consumer,
// and waits for the pump goroutine to exit, bounded by ctx.
func (e *Engine) stopSourceConsumer(ctx context.Context) error {
	e.stopMonitor()
	if consumeCh, tag := e.consumer(); consumeCh != nil && !consumeCh.IsClosed() {
		if err := consumeCh.Cancel(tag, false); err != nil {
			return fmt.Errorf("rabbitmq: cancel source consumer: %w", err)
		}
	}
	if done := e.pumpDoneChan(); done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// stopRunning cancels and joins the pump and quorum-monitor goroutines, if
// any are running.
func (e *Engine) stopRunning() {
	e.runMu.Lock()
	pumpCancel, pumpDone := e.pumpCancel, e.pumpDone
	monitorStop, monitorDone := e.monitorStop, e.monitorDone
	e.runMu.Unlock()

	if pumpCancel != nil {
		pumpCancel()
	}
	if monitorStop != nil {
		monitorStop()
	}
	if pumpDone != nil {
		<-pumpDone
	}
	if monitorDone != nil {
		<-monitorDone
	}
}

// markRunning records this instance as owning the pump, atomically, and
// reports whether it was the one to do so. A false result means a pump is
// already running (or has already finished starting) on this instance, so
// the caller must treat its own Start/Recover as a no-op instead of spawning
// a second consumer and goroutine set.
func (e *Engine) markRunning() bool {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	if e.running {
		return false
	}
	e.running = true
	return true
}

// clearRunning undoes markRunning after a failed setup attempt, so a later
// retry is not permanently treated as a no-op.
func (e *Engine) clearRunning() {
	e.runMu.Lock()
	e.running = false
	e.runMu.Unlock()
}

// haltPump marks the engine unhealthy and immediately stops it from making
// further progress: it cancels the source consumer, so the broker stops
// pushing deliveries and no further message is accepted, then cancels the
// pump loop's context. Unlike setUnhealthy alone, this is a hard stop, not
// just a status flip.
func (e *Engine) haltPump(detail string) {
	e.setUnhealthy(detail)
	if consumeCh, tag := e.consumer(); consumeCh != nil && !consumeCh.IsClosed() {
		_ = consumeCh.Cancel(tag, false)
	}
	e.cancelPump()
}

// dialConn opens a fresh AMQP connection using cfg's URL and TLS, advertising
// name as its client-visible connection_name property for operator
// debugging. The RabbitMQ management API does not key connections by this
// property (see acquireLock), so it is cosmetic, not load-bearing.
func (e *Engine) dialConn(name string) (*amqp.Connection, error) {
	return amqp.DialConfig(e.cfg.URL, amqp.Config{
		TLSClientConfig: e.cfg.TLS,
		Properties:      amqp.Table{"connection_name": name},
	})
}

// ensureConn lazily dials the engine's main connection, reusing it across
// calls. It is safe to call from Prepare (before Start) and again from
// Start/Recover.
func (e *Engine) ensureConn() (*amqp.Connection, error) {
	e.connMu.Lock()
	defer e.connMu.Unlock()
	if e.conn != nil && !e.conn.IsClosed() {
		return e.conn, nil
	}
	conn, err := e.dialConn(fmt.Sprintf("tp-queueagent-%s", e.cfg.QueueName))
	if err != nil {
		return nil, fmt.Errorf("rabbitmq: dial: %w", err)
	}
	e.conn = conn
	return conn, nil
}

// withChannel runs fn with a fresh channel opened on the engine's main
// connection, closing the channel afterward. Every admin RPC (declare,
// delete, inspect) uses its own channel: amqp091-go's channel-level RPCs are
// not safe to interleave from multiple goroutines on one shared channel,
// since the protocol carries no per-request correlation id.
func (e *Engine) withChannel(fn func(*amqp.Channel) error) error {
	conn, err := e.ensureConn()
	if err != nil {
		return err
	}
	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("rabbitmq: open channel: %w", err)
	}
	defer ch.Close()
	return fn(ch)
}

// sessionShadowNameFor returns the bounded deterministic name of routeID's
// session shadow for an engine constructed with cfg.
func sessionShadowNameFor(cfg Config, routeID string) string {
	return queuestate.SessionShadowName(cfg.InstallID, cfg.WorkloadUID, cfg.QueueName, cfg.ActivationID, routeID)
}

// sourceTypeArgs returns the declaration arguments that make a durable
// shadow queue's queue-type match sourceType, always passed explicitly: a
// vhost's default queue type can be changed operator-side, so omitting the
// argument for a classic source would not guarantee a classic shadow.
func sourceTypeArgs(sourceType string) amqp.Table {
	if sourceType == "quorum" {
		return amqp.Table{"x-queue-type": "quorum"}
	}
	return amqp.Table{"x-queue-type": "classic"}
}

const (
	// sourcePrefetch bounds how many source deliveries the broker may push
	// unacked at once. Uncapped prefetch causes RabbitMQ to eagerly push the
	// entire ready backlog (phase 0, R5), which would make "outstanding
	// publishes" at Stop time unbounded.
	sourcePrefetch = 32

	// drainPrefetch bounds a route-drain or app-drain consumer the same way,
	// at a smaller number since draining is not the steady-state path.
	drainPrefetch = 16

	// mgmtPollInterval is the delay between management-API polling attempts.
	// Quorum-queue statistics lag AMQP-visible state by several seconds
	// (phase 0, R4), so callers pair this with a generous deadline rather
	// than a low retry count.
	mgmtPollInterval = 300 * time.Millisecond

	// fenceDeadline bounds how long Start waits while evicting a
	// predecessor's lock connection through the management API.
	fenceDeadline = 30 * time.Second

	// drainIdleTimeout bounds how long a drain consumer (route drain, app
	// drain) waits for the next delivery before concluding the queue has no
	// more ready messages to offer right now.
	drainIdleTimeout = 3 * time.Second

	// quorumStatsStabilityWindow is the minimum gap proveEmpty requires
	// between two zero readings on a quorum queue: management stats were
	// observed lagging AMQP-visible state by about 5s on a default-interval
	// broker (phase 0, R4). This paces DrainApplication only; deletion
	// safety comes from resolveShadow's live passive declare, not this.
	quorumStatsStabilityWindow = 6 * time.Second
)
