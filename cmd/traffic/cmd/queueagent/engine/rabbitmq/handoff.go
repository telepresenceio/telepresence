package rabbitmq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/queueagent/engine"
)

// handoffMarker is the JSON encoding of the Handoff RabbitMQ returns from
// Stop. RabbitMQ has no broker offset to carry across the handback, so the
// marker exists only to make Handoff a recognizable, non-nil, engine-owned
// value: the manager never interprets its content.
type handoffMarker struct {
	ActivationID string    `json:"activationID"`
	QueueName    string    `json:"queueName"`
	StoppedAt    time.Time `json:"stoppedAt"`
}

// Stop cancels the source consumer and waits for every delivery already
// buffered client-side to finish its publish-then-ack cycle, so no
// outstanding publish remains unconfirmed once this returns. RabbitMQ has no
// offset to record as a frontier: new source messages simply wait at the
// broker, unconsumed, until a future activation resumes.
func (e *Engine) Stop(ctx context.Context) (engine.Handoff, error) {
	if err := e.healthErr(); err != nil {
		return nil, err
	}

	if err := e.stopSourceConsumer(ctx); err != nil {
		return nil, err
	}
	if err := e.healthErr(); err != nil {
		return nil, err // the pump may have stopped because it lost the fence, not because of Cancel
	}

	marker := handoffMarker{
		ActivationID: e.cfg.ActivationID,
		QueueName:    e.cfg.QueueName,
		StoppedAt:    time.Now().UTC(),
	}
	data, err := json.Marshal(marker)
	if err != nil {
		return nil, fmt.Errorf("rabbitmq: encode handoff marker: %w", err)
	}
	e.MarkStopped()
	return data, nil
}

// DrainApplication blocks until the app shadow is proven drained by
// proveEmpty. It is bounded by ctx.
func (e *Engine) DrainApplication(ctx context.Context) error {
	return e.proveEmpty(ctx, e.appShadowName)
}

// CommitHandoff performs RabbitMQ's handback: nothing. There is no broker
// offset to move: Stop already cancelled the source consumer, so the
// application resuming the source simply finds it waiting, undisturbed,
// where the queue-agent left off.
func (e *Engine) CommitHandoff(_ context.Context, h engine.Handoff) error {
	var marker handoffMarker
	if err := json.Unmarshal(h, &marker); err != nil {
		return fmt.Errorf("rabbitmq: invalid handoff: %w", err)
	}
	return nil
}

// quorumRetainReason explains, for an operator, why Cleanup leaves a
// verified-empty quorum shadow in place instead of deleting it.
const quorumRetainReason = "quorum queue: broker has no loss-proof conditional delete; awaiting operator reaping"

// VerifyCleanupReady reports whether every shadow this activation owns or
// has retained can be deleted without destroying a message: a live passive
// declare against each, never a management read. A live consumer is a
// plain error; ready residue is a *engine.NeedsDrainError.
func (e *Engine) VerifyCleanupReady(context.Context) error {
	for _, name := range e.shadowsToVerify() {
		messages, consumers, exists, err := e.passiveCounts(name)
		if err != nil {
			return fmt.Errorf("rabbitmq: check %q for cleanup readiness: %w", name, err)
		}
		if !exists {
			continue
		}
		if consumers > 0 {
			return fmt.Errorf("rabbitmq: shadow %q still has %d consumer(s) attached", name, consumers)
		}
		if messages > 0 {
			return &engine.NeedsDrainError{Queue: name, Ready: messages}
		}
	}
	return nil
}

// Cleanup resolves every shadow this activation owns or has retained via
// resolveShadow, refusing while started but neither stopped nor aborted
// (prepare-only is permitted, live checks included). The lock releases
// last: a partial cleanup is a retryable leak, not a held fence.
func (e *Engine) Cleanup(context.Context) ([]engine.RetainedResource, error) {
	if err := e.CleanupGate(); err != nil {
		return nil, fmt.Errorf("rabbitmq: %w", err)
	}

	var errs []error
	var retained []engine.RetainedResource

	for _, name := range e.shadowsToVerify() {
		isRetained, err := e.resolveShadow(name)
		if err != nil {
			errs = append(errs, fmt.Errorf("rabbitmq: cleanup %q: %w", name, err))
			continue
		}
		if isRetained {
			retained = append(retained, engine.RetainedResource{Name: name, Reason: quorumRetainReason})
		}
	}

	e.releaseLock()

	return retained, errors.Join(errs...)
}

// releaseLock closes the engine's connection, if open. The exclusive,
// auto-delete lock queue disappears as a side effect, releasing the fence
// for a future activation. This is Cleanup's terminal step: every shadow
// deletion above needed the connection, so it closes only now.
func (e *Engine) releaseLock() {
	e.connMu.Lock()
	conn := e.conn
	e.conn = nil
	e.connMu.Unlock()
	if conn != nil && !conn.IsClosed() {
		_ = conn.Close()
	}
}
