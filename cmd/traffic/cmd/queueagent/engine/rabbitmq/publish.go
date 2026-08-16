package rabbitmq

import (
	"context"
	"fmt"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/telepresenceio/clog"
)

const (
	publishRetryInitial = 200 * time.Millisecond
	publishRetryMax     = 5 * time.Second
)

// ensurePublishChannel lazily opens the engine's single confirm-mode publish
// channel, reused by the pump, route drain, and handback republishing.
func (e *Engine) ensurePublishChannel() error {
	e.publishMu.Lock()
	defer e.publishMu.Unlock()
	if e.publishCh != nil && !e.publishCh.IsClosed() {
		return nil
	}
	conn, err := e.ensureConn()
	if err != nil {
		return err
	}
	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("rabbitmq: open publish channel: %w", err)
	}
	if err := ch.Confirm(false); err != nil {
		_ = ch.Close()
		return fmt.Errorf("rabbitmq: enable publisher confirms: %w", err)
	}
	e.publishCh = ch
	e.confirms = ch.NotifyPublish(make(chan amqp.Confirmation, 4))
	e.returns = ch.NotifyReturn(make(chan amqp.Return, 4))
	return nil
}

// publishBarrier blocks until no publish is in flight on the shared publish
// channel. Because publishConfirmed serializes publishes to one outstanding
// at a time under publishMu, briefly acquiring and releasing that same mutex
// proves every publish that started before this call has now completed.
//
//nolint:staticcheck // the empty critical section is the barrier itself, not an oversight
func (e *Engine) publishBarrier() {
	e.publishMu.Lock()
	e.publishMu.Unlock()
}

// publishConfirmed publishes msg to queueName on the engine's shared publish
// channel and returns only once the broker has confirmed it with no
// mandatory return. Publishes are serialized to one outstanding at a time:
// dispatch on an AMQP connection is single-threaded per channel, so once this
// call's Confirmation has been received, any basic.return for the same
// publish -- which the broker always sends before the confirm (phase 0, R3)
// -- is already sitting in the buffered returns channel, making a
// non-blocking check after the confirm race-free without stamping message
// content for correlation.
//
// A negative confirm or a return is retried, with backoff, until ctx is
// done: the source delivery this publish protects must not be acknowledged
// until a shadow durably holds a copy (no silent loss).
func (e *Engine) publishConfirmed(ctx context.Context, queueName string, msg amqp.Publishing) error {
	if err := e.ensurePublishChannel(); err != nil {
		return err
	}

	e.publishMu.Lock()
	defer e.publishMu.Unlock()

	backoff := publishRetryInitial
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := e.publishCh.PublishWithContext(ctx, "", queueName, true, false, msg); err != nil {
			return fmt.Errorf("rabbitmq: publish to %q: %w", queueName, err)
		}

		var confirm amqp.Confirmation
		select {
		case c, ok := <-e.confirms:
			if !ok {
				return fmt.Errorf("rabbitmq: publish channel closed while awaiting confirm for %q", queueName)
			}
			confirm = c
		case <-ctx.Done():
			return ctx.Err()
		}

		returned := false
		select {
		case _, ok := <-e.returns:
			returned = ok
		default:
		}

		if confirm.Ack && !returned {
			return nil
		}

		clog.Debugf(ctx, "rabbitmq: publish to %q not accepted (ack=%v returned=%v); retrying", queueName, confirm.Ack, returned)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
		if backoff < publishRetryMax {
			backoff *= 2
		}
	}
}
