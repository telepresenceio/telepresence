package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/telepresenceio/clog"
)

// resourceLockedCode is the AMQP channel-exception code RabbitMQ returns
// when a queue is exclusively held by another connection (phase 0, R2).
const resourceLockedCode = 405

// acquireLock declares the activation's exclusive, auto-delete lock queue on
// the engine's connection, fencing a predecessor. The lock queue and the
// later source consumer deliberately share one connection (see the doc
// comment on Engine.conn): fencing must evict a predecessor's exclusive
// source consumer along with its lock, and RabbitMQ only offers a
// connection-wide eviction primitive. If the lock is currently held,
// acquireLock uses the management API to identify and force-close the
// holder's whole connection -- the newer activation always wins, and the
// older one notices (via its own connection-close watcher) and reports
// itself unhealthy. acquireLock gives up and returns an error if it cannot
// acquire the lock, by any means, before fenceDeadline.
func (e *Engine) acquireLock(ctx context.Context) error {
	e.lockMu.Lock()
	held := e.lockHeld
	e.lockMu.Unlock()
	if held {
		return nil // already held by this instance
	}

	if _, err := e.ensureConn(); err != nil {
		return err
	}

	deadline := time.Now().Add(fenceDeadline)
	for {
		declareErr := e.withChannel(func(ch *amqp.Channel) error {
			_, err := ch.QueueDeclare(e.lockName, false, true, true, false, nil)
			return err
		})
		if declareErr == nil {
			e.lockMu.Lock()
			e.lockHeld = true
			e.lockMu.Unlock()
			e.watchConnForFencing()
			return nil
		}
		if !isResourceLocked(declareErr) {
			return fmt.Errorf("rabbitmq: declare lock queue %q: %w", e.lockName, declareErr)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("rabbitmq: lock queue %q held by a predecessor that could not be fenced within %s: %w",
				e.lockName, fenceDeadline, declareErr)
		}
		clog.Debugf(ctx, "rabbitmq: lock queue %q is held; attempting to fence the holder", e.lockName)
		if evictErr := e.evictLockHolder(ctx); evictErr != nil {
			clog.Debugf(ctx, "rabbitmq: fencing attempt for %q did not complete: %v", e.lockName, evictErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(mgmtPollInterval):
		}
	}
}

// evictLockHolder reads the lock queue's current owning connection from the
// management API and force-closes it -- which, since the holder's source
// consumer lives on that same connection, also releases its exclusive
// consume. It tolerates the owner not yet being reported (management stats
// lag AMQP-visible state, phase 0 R4) by simply returning; the caller's
// retry loop will try again.
func (e *Engine) evictLockHolder(ctx context.Context) error {
	doc, err := e.mgmt.getQueue(ctx, e.lockName)
	if err != nil {
		if isNotFound(err) {
			return nil // released between our declare attempt and this read
		}
		return err
	}
	owner := doc.OwnerPidDetails.Name
	if owner == "" {
		return nil // not yet visible via management stats
	}
	return e.mgmt.closeConnection(ctx, owner)
}

// watchConnForFencing starts a goroutine that marks the engine unhealthy the
// moment its connection closes for any reason other than this engine's own
// orderly release (Cleanup or Close, which close it deliberately once
// nothing further should run). A connection that closes any other way means
// a newer instance has fenced this one out from under it.
func (e *Engine) watchConnForFencing() {
	e.connMu.Lock()
	conn := e.conn
	e.connMu.Unlock()
	if conn == nil {
		return
	}
	closed := conn.NotifyClose(make(chan *amqp.Error, 1))
	e.lockWatchWG.Add(1)
	go func() {
		defer e.lockWatchWG.Done()
		reason, ok := <-closed
		if !ok || reason == nil {
			return // orderly close (Cleanup/Close already released the fence)
		}
		e.haltPump(fmt.Sprintf("connection closed: %v", reason))
	}()
}

// isResourceLocked reports whether err is the AMQP RESOURCE_LOCKED channel
// exception RabbitMQ returns when a queue is exclusively held elsewhere.
func isResourceLocked(err error) bool {
	var amqpErr *amqp.Error
	if errors.As(err, &amqpErr) {
		return amqpErr.Code == resourceLockedCode
	}
	return false
}
