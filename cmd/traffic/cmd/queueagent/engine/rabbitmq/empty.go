package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// proveEmpty blocks until name is proven to hold neither ready nor
// unacknowledged messages. It paces DrainApplication's wait for the
// redirected application to finish draining; it is not a deletion guard --
// resolveShadow's live passive declare is what authorizes a delete. Bounded
// by ctx.
func (e *Engine) proveEmpty(ctx context.Context, name string) error {
	quorum := e.sourceType == "quorum"
	var zeroSince time.Time
	for {
		empty, err := e.emptyRound(ctx, name)
		if err != nil {
			return fmt.Errorf("prove %q empty: %w", name, err)
		}
		switch {
		case !empty:
			zeroSince = time.Time{}
		case !quorum:
			return nil
		case zeroSince.IsZero():
			zeroSince = time.Now()
		case time.Since(zeroSince) >= quorumStatsStabilityWindow:
			return nil
		}
		select {
		case <-time.After(mgmtPollInterval):
		case <-ctx.Done():
			return fmt.Errorf("prove %q empty: %w", name, ctx.Err())
		}
	}
}

// emptyRound reads name's ready count via an AMQP passive declare and its
// unacknowledged count via the management API, in the same round, and
// reports whether both are zero. A management 404 is treated as not-yet-zero
// rather than an error: the queue already reads as existing via the passive
// declare above, so a 404 here is stats collection not yet caught up, not
// absence.
func (e *Engine) emptyRound(ctx context.Context, name string) (bool, error) {
	messages, err := e.passiveMessages(name)
	if err != nil {
		return false, err
	}
	if messages != 0 {
		return false, nil
	}
	doc, err := e.mgmt.getQueue(ctx, name)
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("inspect %q: %w", name, err)
	}
	return doc.MessagesUnacknowledged == 0, nil
}

// passiveCounts returns name's live ready-message and consumer counts via an
// AMQP passive declare on a fresh channel, the broker's own count with none
// of the management API's stats-collection lag, plus whether name exists at
// all. A missing queue reads as (0, 0, false, nil), not an error.
func (e *Engine) passiveCounts(name string) (messages, consumers int, exists bool, err error) {
	conn, err := e.ensureConn()
	if err != nil {
		return 0, 0, false, err
	}
	ch, err := conn.Channel()
	if err != nil {
		return 0, 0, false, fmt.Errorf("open channel: %w", err)
	}
	defer ch.Close()

	q, err := ch.QueueDeclarePassive(name, false, false, false, false, nil)
	if err != nil {
		var amqpErr *amqp.Error
		if errors.As(err, &amqpErr) && amqpErr.Code == amqp.NotFound {
			return 0, 0, false, nil
		}
		return 0, 0, false, fmt.Errorf("passive declare %q: %w", name, err)
	}
	return q.Messages, q.Consumers, true, nil
}

// passiveMessages returns name's live ready-message count; see
// passiveCounts. A missing queue reads as 0, not an error.
func (e *Engine) passiveMessages(name string) (int, error) {
	messages, _, _, err := e.passiveCounts(name)
	return messages, err
}

// resolveShadow verifies name empty via a live passive declare, then either
// deletes it (classic) or reports it retained (quorum, which has no
// loss-proof conditional delete). A missing name is a no-op. A live
// consumer or ready message fails closed: name is left in place, not
// deleted, and not reported retained.
func (e *Engine) resolveShadow(name string) (retained bool, err error) {
	messages, consumers, exists, err := e.passiveCounts(name)
	if err != nil {
		return false, fmt.Errorf("check %q: %w", name, err)
	}
	if !exists {
		return false, nil
	}
	if consumers > 0 || messages > 0 {
		return false, fmt.Errorf("%q not safe to delete: %d consumer(s), %d ready message(s)", name, consumers, messages)
	}
	if e.sourceType == "quorum" {
		return true, nil
	}
	return false, e.deleteClassicShadow(name)
}

// deleteClassicShadow deletes name with ifUnused and ifEmpty. ifUnused
// guards only an active basic.consume; a delivery parked on a basic.get
// hold is invisible to it and would be destroyed, but the caller's causal
// quiescence (phase 0, R7) has already ruled that out before this runs.
func (e *Engine) deleteClassicShadow(name string) error {
	return e.withChannel(func(ch *amqp.Channel) error {
		_, err := ch.QueueDelete(name, true, true, false)
		return err
	})
}
