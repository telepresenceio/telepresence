package rabbitmq

import (
	"context"
	"fmt"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/telepresenceio/clog"
)

// Start acquires the provider fence for this activation, verifies via the
// management API that no foreign consumer is attached to the source, then
// begins pumping it at its current position (there is no separate "final
// application position" to seek to for RabbitMQ: unlike Kafka's committed
// offsets, a durable queue's position is simply whatever it has ready). A
// second Start on an already-running instance is a no-op.
func (e *Engine) Start(ctx context.Context) error {
	if err := e.healthErr(); err != nil {
		return err
	}
	if !e.markRunning() {
		return nil // already running: idempotent no-op
	}
	if err := e.acquireLock(ctx); err != nil {
		e.clearRunning()
		return err
	}
	if err := e.checkSourceNoConsumers(ctx); err != nil {
		e.clearRunning()
		return err
	}
	if err := e.startPump(ctx); err != nil {
		e.clearRunning()
		return err
	}
	e.started.Store(true)
	return nil
}

// checkSourceNoConsumers verifies, via the management API, that no consumer
// is attached to the source queue. It polls up to fenceDeadline instead of
// reading once, tolerating management-API stats lag behind a just-evicted
// predecessor's connection (phase 0, R4), and fails with a clear error if a
// foreign consumer is still attached once the deadline passes.
func (e *Engine) checkSourceNoConsumers(ctx context.Context) error {
	if _, err := e.mgmt.waitForQueue(ctx, e.cfg.Source, func(d *queueDoc) bool {
		return d.Consumers == 0
	}); err != nil {
		return fmt.Errorf("rabbitmq: source queue %q still has a consumer attached: %w", e.cfg.Source, err)
	}
	return nil
}

// startPump opens the source consumer and launches the pump goroutine. The
// pump itself runs detached from ctx (ctx bounds only this call's setup),
// stopping instead via Stop's consumer cancellation, a lost fence, or Close.
// If any step fails after the source consumer is opened, everything opened
// so far is unwound before returning the error.
func (e *Engine) startPump(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	conn, err := e.ensureConn()
	if err != nil {
		return err
	}
	consumeCh, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("rabbitmq: open source consumer channel: %w", err)
	}
	ready := false
	defer func() {
		if !ready {
			_ = consumeCh.Close() // unwind: setup failed after this channel was opened
		}
	}()

	if err := consumeCh.Qos(sourcePrefetch, 0, false); err != nil {
		return fmt.Errorf("rabbitmq: set source prefetch: %w", err)
	}

	consumerTag := fmt.Sprintf("tp-source-%s", e.cfg.QueueName)
	// Consume, not ConsumeWithContext: the consumer must outlive this call's
	// ctx, which bounds only setup, not the pump's lifetime (see the
	// detached pumpCtx below).
	deliveries, err := consumeCh.Consume(e.cfg.Source, consumerTag, false, true, false, false, nil)
	if err != nil {
		return fmt.Errorf("rabbitmq: consume source %q: %w", e.cfg.Source, err)
	}

	if err := e.ensurePublishChannel(); err != nil {
		_ = consumeCh.Cancel(consumerTag, false) // unwind the consumer opened above
		return err
	}

	pumpCtx, pumpCancel := context.WithCancel(context.Background())
	pumpDone := make(chan struct{})
	go e.runPump(pumpCtx, deliveries, pumpDone)

	var monitorStop context.CancelFunc
	var monitorDone chan struct{}
	if e.sourceType == "quorum" {
		monitorCtx, cancel := context.WithCancel(context.Background())
		monitorStop = cancel
		monitorDone = make(chan struct{})
		go e.monitorQuorumSource(monitorCtx, monitorDone)
	}

	e.setRun(consumeCh, consumerTag, pumpCancel, pumpDone, monitorStop, monitorDone)
	ready = true
	return nil
}

// runPump reads source deliveries until the deliveries channel closes (after
// Stop cancels the consumer and every buffered delivery has been drained) or
// ctx is cancelled (a lost fence or Close). Every delivery is classified,
// published to its destination with confirmation, and only then acked --
// the durable-copy-before-ack contract that makes a crash between them at
// worst duplicate a message, never lose one.
func (e *Engine) runPump(ctx context.Context, deliveries <-chan amqp.Delivery, done chan struct{}) {
	defer close(done)
	for {
		select {
		case d, ok := <-deliveries:
			if !ok {
				return
			}
			if err := e.pumpOne(ctx, d); err != nil {
				if ctx.Err() == nil {
					e.setUnhealthy(fmt.Sprintf("pump: %v", err))
					clog.Errorf(ctx, "rabbitmq: pump for queue %q stopped: %v", e.cfg.QueueName, err)
				}
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// pumpOne classifies and forwards one source delivery, acking it only after
// its shadow publish is confirmed.
func (e *Engine) pumpOne(ctx context.Context, d amqp.Delivery) error {
	dest := e.classify(d.Headers)
	if err := e.publishConfirmed(ctx, dest, forwardedPublishing(d)); err != nil {
		return err
	}
	return d.Ack(false)
}

// forwardedPublishing copies d's portable properties and body into a
// Publishing for republish to a shadow. UserId is deliberately not carried
// over: AMQP requires a published user-id property to match the publishing
// connection's authenticated identity, and the queue-agent's broker identity
// is not the original publisher's, so copying it would turn every
// republish into a broker-rejected publish.
func forwardedPublishing(d amqp.Delivery) amqp.Publishing {
	return amqp.Publishing{
		Headers:         d.Headers,
		ContentType:     d.ContentType,
		ContentEncoding: d.ContentEncoding,
		DeliveryMode:    d.DeliveryMode,
		Priority:        d.Priority,
		CorrelationId:   d.CorrelationId,
		ReplyTo:         d.ReplyTo,
		Expiration:      d.Expiration,
		MessageId:       d.MessageId,
		Timestamp:       d.Timestamp,
		Type:            d.Type,
		AppId:           d.AppId,
		Body:            d.Body,
	}
}

// classify returns the destination shadow name for a message with the given
// headers: the first route (in ReconcileRoutes's given order) whose filter
// matches, or the app shadow when none does. The manager guarantees routes
// are pairwise non-overlapping, so match order does not change behavior for
// a valid route table; it is still deterministic for one that (transiently,
// or in a test) is not.
func (e *Engine) classify(headers amqp.Table) string {
	msgHeaders := stringHeaders(headers)

	e.routesMu.Lock()
	defer e.routesMu.Unlock()
	for _, r := range e.routes {
		if matchFilter(msgHeaders, r.Filter) {
			return e.sessionShadowName(r.ID)
		}
	}
	return e.appShadowName
}

// stringHeaders returns the string-valued entries of headers. Non-string
// AMQP header values never match a route filter (plan: filter semantics).
func stringHeaders(headers amqp.Table) map[string]string {
	out := make(map[string]string, len(headers))
	for k, v := range headers {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// matchFilter reports whether every key in filter maps to the same value in
// headers. An empty filter matches every message.
func matchFilter(headers, filter map[string]string) bool {
	for k, v := range filter {
		if headers[k] != v {
			return false
		}
	}
	return true
}

// maxQuorumMonitorFailures bounds consecutive management-API failures
// monitorQuorumSource tolerates before treating ownership as unverifiable.
// At one tick every mgmtPollInterval*20 (~6s), this is ~30s without a
// successful reading.
const maxQuorumMonitorFailures = 5

// monitorQuorumSource polls the management API for the source queue's
// consumer count, halting the pump if it is ever anything other than 1: on a
// quorum queue the exclusive consume flag is not enforced (phase 0, R1), so
// a foreign consumer can join silently, and this is the only way to detect
// that violation of the documented single-consumer precondition. A
// transient management-API failure is tolerated, but maxQuorumMonitorFailures
// consecutive failures halt the pump too: ownership can no longer be
// verified either way, so silently continuing would fail open.
func (e *Engine) monitorQuorumSource(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(mgmtPollInterval * 20) // quorum stats lag ~5s (phase 0, R4)
	defer ticker.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			doc, err := e.mgmt.getQueue(ctx, e.cfg.Source)
			if err != nil {
				failures++
				if failures >= maxQuorumMonitorFailures {
					e.haltPump(fmt.Sprintf(
						"quorum source %q: %d consecutive management-API failures, ownership can no longer be verified: %v",
						e.cfg.Source, failures, err))
					return
				}
				continue // transient management-API failure; keep monitoring
			}
			failures = 0
			if doc.Consumers != 1 {
				e.haltPump(fmt.Sprintf(
					"quorum source %q reports %d consumers, expected exactly 1 (foreign consumer detected)",
					e.cfg.Source, doc.Consumers))
				return
			}
		}
	}
}

// sessionShadowName returns the bounded deterministic name of routeID's
// session shadow under this engine's identity.
func (e *Engine) sessionShadowName(routeID string) string {
	return sessionShadowNameFor(e.cfg, routeID)
}
