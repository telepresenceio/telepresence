package rabbitmq

import (
	"context"
	"fmt"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/queueagent/engine"
)

// Prepare reads the source queue's type and declaration arguments through
// the management API, rejects a shape this engine cannot safely split, and
// declares the durable app shadow with the source's queue type. It performs
// no consumption and takes no fence. It is idempotent: re-declaring an
// already-existing app shadow with identical arguments is a no-op success.
func (e *Engine) Prepare(ctx context.Context) (engine.EnvOverrides, error) {
	if err := e.healthErr(); err != nil {
		return nil, err
	}

	// Poll rather than read once: the management plugin's queue listing is
	// populated from periodically collected stats, so a queue declared just
	// before Prepare runs (as in a fresh activation racing the app's own
	// startup) can 404 briefly even though the broker already has it.
	doc, err := e.mgmt.waitForQueue(ctx, e.cfg.Source, func(*queueDoc) bool { return true })
	if err != nil {
		return nil, fmt.Errorf("rabbitmq: source queue %q does not exist or could not be inspected: %w", e.cfg.Source, err)
	}
	if err := validateSourceQueue(doc); err != nil {
		return nil, fmt.Errorf("rabbitmq: source queue %q: %w", e.cfg.Source, err)
	}

	e.sourceType = doc.Type

	if err := e.declareShadow(e.appShadowName); err != nil {
		return nil, fmt.Errorf("rabbitmq: declare app shadow %q: %w", e.appShadowName, err)
	}

	return engine.EnvOverrides{e.cfg.SourceEnvName: e.appShadowName}, nil
}

// validateSourceQueue rejects a source shape this engine cannot safely
// split: anything other than a durable classic or quorum queue, and any
// declaration argument beyond x-queue-type (TTL, priority, dead-letter, and
// similar are all out of scope for v1).
func validateSourceQueue(doc *queueDoc) error {
	if doc.Type != "classic" && doc.Type != "quorum" {
		return fmt.Errorf("unsupported queue type %q (only classic and quorum sources are supported)", doc.Type)
	}
	if !doc.Durable {
		return fmt.Errorf("source queue must be durable")
	}
	if doc.Exclusive {
		return fmt.Errorf("source queue must not be exclusive")
	}
	if doc.AutoDelete {
		return fmt.Errorf("source queue must not be auto-delete")
	}
	for k := range doc.Arguments {
		if k != "x-queue-type" {
			return fmt.Errorf("unsupported declaration argument %q (only x-queue-type is supported)", k)
		}
	}
	return nil
}
