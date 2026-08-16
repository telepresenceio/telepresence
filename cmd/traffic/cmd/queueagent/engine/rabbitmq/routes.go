package rabbitmq

import (
	"context"
	"fmt"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/queueagent/engine"
)

// ReconcileRoutes installs routes as the desired route table: it declares a
// durable session shadow for every route not previously seen, then swaps the
// table used by the pump's classifier. It never deletes a shadow.
func (e *Engine) ReconcileRoutes(ctx context.Context, routes []engine.Route) error {
	if err := e.healthErr(); err != nil {
		return err
	}

	e.routesMu.Lock()
	known := make(map[string]bool, len(e.routes))
	for _, r := range e.routes {
		known[r.ID] = true
	}
	e.routesMu.Unlock()

	for _, r := range routes {
		if known[r.ID] {
			continue
		}
		if err := e.declareShadow(e.sessionShadowName(r.ID)); err != nil {
			return fmt.Errorf("rabbitmq: declare session shadow for route %q: %w", r.ID, err)
		}
	}

	desired := make([]engine.Route, len(routes))
	copy(desired, routes)

	e.routesMu.Lock()
	e.routes = desired
	e.routesMu.Unlock()
	return nil
}

// declareShadow declares a durable shadow queue of the source's queue type.
// Declaring an already-existing queue with identical arguments is a
// no-op success, so this is safe to call repeatedly (Prepare's own
// idempotency, and ReconcileRoutes/Recover's re-declares, rely on it).
func (e *Engine) declareShadow(name string) error {
	args := sourceTypeArgs(e.sourceType)
	return e.withChannel(func(ch *amqp.Channel) error {
		_, err := ch.QueueDeclare(name, true, false, false, false, args)
		return err
	})
}

// DrainRoute stops classifying messages to id, waits for the publish
// barrier, moves the route's unconsumed residue to the app shadow, and
// deletes its session shadow only after proof that it is empty. A session
// shadow that no longer exists -- a retry after a prior call already deleted
// it -- makes DrainRoute a no-op success. A quorum shadow verified empty has
// no loss-proof delete, so it is left in place and returned as retained
// instead, for the caller to persist into RecoveredState.Retained.
func (e *Engine) DrainRoute(ctx context.Context, id string) ([]engine.RetainedResource, error) {
	if err := e.healthErr(); err != nil {
		return nil, err
	}

	shadow := e.sessionShadowName(id)

	e.routesMu.Lock()
	kept := e.routes[:0:0]
	for _, r := range e.routes {
		if r.ID != id {
			kept = append(kept, r)
		}
	}
	e.routes = kept
	e.routesMu.Unlock()

	// Publish barrier: any publish already classified to this route -- which
	// must have started before the route removal above became visible, or
	// it would not have selected this route at all -- completes before
	// publishBarrier returns.
	e.publishBarrier()

	// Check existence first: an already-gone shadow must never be polled
	// toward fenceDeadline, since it will never satisfy the
	// consumers-dropped-to-0 condition below.
	exists, err := e.mgmt.queueExists(ctx, shadow)
	if err != nil {
		return nil, fmt.Errorf("rabbitmq: check route %q shadow before drain: %w", id, err)
	}
	if !exists {
		return nil, nil
	}

	if _, err := e.mgmt.waitForQueue(ctx, shadow, func(d *queueDoc) bool {
		return d.Consumers == 0
	}); err != nil {
		return nil, fmt.Errorf("rabbitmq: wait for route %q consumers to drop to 0: %w", id, err)
	}

	if err := e.drainQueueToShadow(ctx, shadow, e.appShadowName); err != nil {
		return nil, fmt.Errorf("rabbitmq: drain route %q residue to app shadow: %w", id, err)
	}

	retained, err := e.resolveShadow(shadow)
	if err != nil {
		return nil, fmt.Errorf("rabbitmq: finalize route %q shadow: %w", id, err)
	}
	if !retained {
		return nil, nil
	}
	e.routesMu.Lock()
	e.retainedShadows[shadow] = true
	e.routesMu.Unlock()
	return []engine.RetainedResource{{Name: shadow, Reason: quorumRetainReason}}, nil
}

// shadowsToVerify returns the app shadow, every current route's session
// shadow, and every quorum shadow DrainRoute or Cleanup previously
// retained -- the full set VerifyCleanupReady and Cleanup must account for.
func (e *Engine) shadowsToVerify() []string {
	e.routesMu.Lock()
	defer e.routesMu.Unlock()
	names := make([]string, 0, len(e.routes)+len(e.retainedShadows)+1)
	names = append(names, e.appShadowName)
	for _, r := range e.routes {
		names = append(names, e.sessionShadowName(r.ID))
	}
	for name := range e.retainedShadows {
		names = append(names, name)
	}
	return names
}

// drainQueueToShadow consumes every ready message from src and republishes
// each, with confirmation, to dst, acking the src delivery only once its
// republish is confirmed. It cancels its consumer before the loop's final
// ack, and before returning, so a freed prefetch slot cannot pull in a
// message published back into src by a concurrent rollback (phase 0, R5).
func (e *Engine) drainQueueToShadow(ctx context.Context, src, dst string) error {
	conn, err := e.ensureConn()
	if err != nil {
		return err
	}
	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("open drain channel: %w", err)
	}
	defer ch.Close()
	if err := ch.Qos(drainPrefetch, 0, false); err != nil {
		return fmt.Errorf("set drain prefetch: %w", err)
	}

	tag := "tp-drain-" + src
	deliveries, err := ch.Consume(src, tag, false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("consume %q for drain: %w", src, err)
	}

	for {
		var d amqp.Delivery
		select {
		case msg, ok := <-deliveries:
			if !ok {
				return nil
			}
			d = msg
		case <-time.After(drainIdleTimeout):
			_ = ch.Cancel(tag, false)
			return nil
		case <-ctx.Done():
			_ = ch.Cancel(tag, false)
			return ctx.Err()
		}

		if err := e.publishConfirmed(ctx, dst, forwardedPublishing(d)); err != nil {
			_ = ch.Cancel(tag, false)
			return err
		}
		if err := d.Ack(false); err != nil {
			_ = ch.Cancel(tag, false)
			return fmt.Errorf("ack drained delivery: %w", err)
		}
	}
}
