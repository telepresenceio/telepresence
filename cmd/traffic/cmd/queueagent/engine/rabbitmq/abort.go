package rabbitmq

import (
	"context"
	"fmt"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/queueagent/engine"
)

// Abort unwinds a started activation without a handback: it stops source
// consumption, then republishes every message currently sitting in the app
// shadow and every session shadow back to the source queue, with publisher
// confirms, acking each shadow delivery only after its confirm. An absent or
// already-empty shadow is a no-op, so a retry after a partial failure is
// safe; duplicates are possible, loss is not.
func (e *Engine) Abort(ctx context.Context) error {
	if err := e.healthErr(); err != nil {
		return err
	}

	if err := e.stopSourceConsumer(ctx); err != nil {
		return err
	}

	if err := e.rollbackShadowToSource(ctx, e.appShadowName); err != nil {
		return fmt.Errorf("rabbitmq: roll back app shadow: %w", err)
	}

	e.routesMu.Lock()
	routes := make([]engine.Route, len(e.routes))
	copy(routes, e.routes)
	e.routesMu.Unlock()

	for _, r := range routes {
		if err := e.rollbackShadowToSource(ctx, e.sessionShadowName(r.ID)); err != nil {
			return fmt.Errorf("rabbitmq: roll back session shadow for route %q: %w", r.ID, err)
		}
	}
	e.MarkAborted()
	return nil
}

// rollbackShadowToSource republishes every message currently in shadow back
// to the source queue, with confirms, acking each shadow delivery only after
// its confirm. An absent shadow is a no-op, making Abort idempotent.
func (e *Engine) rollbackShadowToSource(ctx context.Context, shadow string) error {
	exists, err := e.mgmt.queueExists(ctx, shadow)
	if err != nil {
		return fmt.Errorf("check %q before rollback: %w", shadow, err)
	}
	if !exists {
		return nil
	}
	return e.drainQueueToShadow(ctx, shadow, e.cfg.Source)
}
