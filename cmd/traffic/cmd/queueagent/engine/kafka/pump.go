package kafka

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/queueagent/engine"
)

// sessionTimeout bounds how long the splitter group's static membership
// slot outlives an unresponsive process before the broker frees it. A
// replacement joining under the same instance ID reclaims the slot
// immediately regardless of this timeout; it only matters while no
// replacement has joined yet.
const sessionTimeout = 10 * time.Second

// produceCommitTimeout bounds one classify-produce-commit cycle once it is
// decoupled from the pump's poll context (see processRecord), so a cycle
// already in flight when Stop cancels the poll context still finishes
// instead of aborting mid-produce.
const produceCommitTimeout = 30 * time.Second

// ownerCheckInterval bounds how often the ownership monitor polls the
// original app group's membership while the pump is running.
const ownerCheckInterval = 3 * time.Second

// ownerCheckRPCTimeout bounds one ownership-monitor DescribeGroups call.
const ownerCheckRPCTimeout = 5 * time.Second

// ownerCheckFailureBudget bounds how many consecutive ownership-check
// failures the monitor tolerates before treating verification as lost. A
// fixed failure count, rather than an elapsed-time budget, composes
// directly with ownerCheckInterval: the monitor halts within
// ownerCheckFailureBudget*ownerCheckInterval of verification last
// succeeding.
const ownerCheckFailureBudget = 5

// Start acquires clients and begins pumping through startPump, which -- once
// the original app group is confirmed unowned -- seeds the splitter group's
// starting offset from that group's position at this moment: "the
// application's final position" per the contract, captured here rather than
// at Prepare time so a long gap (or a restart) between Prepare and Start
// does not resume from a stale position. Seeding only runs after the
// ownership check passes, so a Start rejected for active ownership leaves no
// seed behind for a later retry to resume from; seeding is idempotent, so
// the legitimate retry-after-fence case seeds correctly at that later
// moment. It also only fills in a partition the splitter group does not
// already have a committed offset for, so a resumed splitter's real
// progress (recovered via Recover, not this path) is never overwritten.
func (e *Engine) Start(ctx context.Context) error {
	e.mu.Lock()
	err := e.ensureClients()
	e.mu.Unlock()
	if err != nil {
		return err
	}
	if err := e.startPump(ctx); err != nil {
		return err
	}
	e.MarkStarted()
	return nil
}

// startPump verifies the original app group is unowned, seeds the splitter
// group's starting offset from that group's current position, fences the
// producer, joins the splitter group under the static instance ID, and
// starts the pump and ownership-monitor goroutines. It is a no-op if the
// pump is already running. Recover also calls this to resume an activation
// whose splitter already has committed progress, for which seeding is a
// no-op.
func (e *Engine) startPump(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.pumpCancel != nil {
		return nil
	}
	if err := e.ensureClients(); err != nil {
		return err
	}
	if err := e.checkOwnershipLocked(ctx); err != nil {
		return err
	}
	if err := e.seedSplitterOffsets(ctx); err != nil {
		return err
	}
	if err := e.fenceProducerLocked(ctx); err != nil {
		return err
	}

	opts, err := e.baseClientOpts()
	if err != nil {
		return err
	}
	opts = append(opts,
		kgo.ConsumerGroup(e.splitterGroup),
		kgo.ConsumeTopics(e.cfg.Source),
		kgo.InstanceID(e.instanceID),
		kgo.SessionTimeout(sessionTimeout),
		kgo.DisableAutoCommit(),
		kgo.FetchIsolationLevel(kgo.ReadCommitted()),
	)
	consumer, err := kgo.NewClient(opts...)
	if err != nil {
		return fmt.Errorf("kafka: building splitter consumer: %w", err)
	}
	e.consumer = consumer
	e.stopRequested.Store(false)

	pumpCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	e.pumpCancel = cancel
	e.pumpDone = done
	go e.runPump(pumpCtx, consumer, done)

	monCtx, monCancel := context.WithCancel(context.Background())
	monDone := make(chan struct{})
	e.monitorCancel = monCancel
	e.monitorDone = monDone
	go e.watchOwnership(monCtx, done, monDone)
	return nil
}

// checkOwnershipLocked fails if the original app group still has a live
// member: splitting must never start while the application is itself
// consuming the source. The caller must hold mu.
func (e *Engine) checkOwnershipLocked(ctx context.Context) error {
	n, err := describeGroupMembers(ctx, e.admin, e.cfg.Group)
	if err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf(
			"kafka: cannot start splitting: original consumer group %q has %d active member(s)", e.cfg.Group, n,
		)
	}
	return nil
}

// appGroupMemberCount snapshots the admin client and reports the original
// app group's current member count.
func (e *Engine) appGroupMemberCount(ctx context.Context) (int, error) {
	e.mu.Lock()
	admin := e.admin
	e.mu.Unlock()
	if admin == nil {
		return 0, errors.New("kafka: engine is closed")
	}
	return describeGroupMembers(ctx, admin, e.cfg.Group)
}

// describeGroupMembers returns group's current member count, treating an
// absent or dead group as zero members.
func describeGroupMembers(ctx context.Context, admin *kadm.Client, group string) (int, error) {
	groups, err := admin.DescribeGroups(ctx, group)
	if err != nil {
		return 0, fmt.Errorf("kafka: describing consumer group %q: %w", group, err)
	}
	return groupMemberCount(groups, group)
}

// groupMemberCount extracts group's member count from a DescribeGroups
// result, treating an absent or dead group as zero members.
func groupMemberCount(groups kadm.DescribedGroups, group string) (int, error) {
	g, ok := groups[group]
	if !ok {
		return 0, nil
	}
	if g.Err != nil {
		if isUnknownGroupErr(g.Err) {
			return 0, nil
		}
		return 0, fmt.Errorf("kafka: describing consumer group %q: %w", group, g.Err)
	}
	return len(g.Members), nil
}

// watchOwnership polls the original app group's membership while the pump
// runs and halts the pump the moment a member appears: the application must
// never consume the source concurrently with the splitter. A transient
// DescribeGroups failure is tolerated, but ownerCheckFailureBudget
// consecutive failures halt the pump too: past that point ownership can no
// longer be verified, so continuing to pump would be running blind.
func (e *Engine) watchOwnership(ctx context.Context, pumpDone <-chan struct{}, done chan struct{}) {
	defer close(done)

	ticker := time.NewTicker(ownerCheckInterval)
	defer ticker.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-pumpDone:
			return
		case <-ticker.C:
		}

		checkCtx, cancel := context.WithTimeout(ctx, ownerCheckRPCTimeout)
		n, err := e.appGroupMemberCount(checkCtx)
		cancel()
		if err != nil {
			failures++
			if failures >= ownerCheckFailureBudget {
				e.haltForLostVerification(failures, err)
				return
			}
			continue // transient; retried next tick
		}
		failures = 0
		if n > 0 {
			e.haltForOwnershipViolation(n)
			return
		}
	}
}

// haltPump cancels and waits for the pump goroutine, then marks the engine
// unhealthy with err. It runs on the ownership-monitor goroutine and must
// not wait on monitorDone -- that would deadlock against its own exit.
func (e *Engine) haltPump(err error) {
	e.mu.Lock()
	cancel := e.pumpCancel
	done := e.pumpDone
	e.pumpCancel = nil
	e.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	e.setUnhealthy(err)
}

// haltForOwnershipViolation halts the pump once the original app group has
// gained a member while splitting is active.
func (e *Engine) haltForOwnershipViolation(members int) {
	e.haltPump(fmt.Errorf(
		"kafka: original consumer group %q gained %d active member(s) while splitting is active", e.cfg.Group, members,
	))
}

// haltForLostVerification halts the pump once ownerCheckFailureBudget
// consecutive DescribeGroups failures leave ownership unverifiable.
func (e *Engine) haltForLostVerification(failures int, lastErr error) {
	e.haltPump(fmt.Errorf(
		"kafka: ownership can no longer be verified: %d consecutive checks of consumer group %q failed: %w",
		failures, e.cfg.Group, lastErr,
	))
}

// fenceProducerLocked forces a fresh producer epoch for this activation's
// transactional ID before the splitter consumer joins, so a predecessor
// process cannot successfully produce or commit after this call returns.
// The caller must hold mu.
func (e *Engine) fenceProducerLocked(ctx context.Context) error {
	e.producerMu.Lock()
	defer e.producerMu.Unlock()

	if err := e.producer.BeginTransaction(); err != nil {
		return fmt.Errorf("kafka: fencing producer: %w", err)
	}
	if err := e.producer.EndTransaction(ctx, kgo.TryAbort); err != nil {
		return fmt.Errorf("kafka: fencing producer: %w", err)
	}
	return nil
}

// runPump consumes the source one record at a time, classifies and forwards
// each to its destination shadow, and commits the source offset only after
// the forward is acknowledged. It exits, marking the engine unhealthy, on a
// fencing error or a genuine processing failure, and exits silently when
// ctx is canceled -- including when stopRequested marks that cancellation
// as an orderly Stop rather than a fault.
func (e *Engine) runPump(ctx context.Context, consumer *kgo.Client, done chan struct{}) {
	defer close(done)

	for {
		if e.stopRequested.Load() {
			return
		}
		fetches := consumer.PollRecords(ctx, 1)
		if ctx.Err() != nil {
			return
		}
		if err := fetches.Err(); err != nil {
			if isFencedInstanceErr(err) {
				e.setUnhealthy(fmt.Errorf("kafka: splitter consumer fenced: %w", err))
				return
			}
			if isOrderlyShutdownErr(err) {
				return
			}
			// Transient broker/network error: back off and retry.
			time.Sleep(250 * time.Millisecond)
			continue
		}
		recs := fetches.Records()
		if len(recs) == 0 {
			continue
		}
		if err := e.processRecord(ctx, consumer, recs[0]); err != nil {
			if e.stopRequested.Load() && isOrderlyShutdownErr(err) {
				return
			}
			e.setUnhealthy(err)
			return
		}
	}
}

// processRecord classifies rec against the installed route table, produces
// it to the destination shadow, and commits its source offset only after
// the produce is acknowledged. The produce-commit cycle runs on a context
// decoupled from ctx's cancellation (bounded instead by
// produceCommitTimeout), so a cycle already under way when Stop cancels the
// pump's poll context still finishes instead of aborting mid-produce.
func (e *Engine) processRecord(ctx context.Context, consumer *kgo.Client, rec *kgo.Record) error {
	e.producerMu.Lock()
	defer e.producerMu.Unlock()

	cycleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), produceCommitTimeout)
	defer cancel()

	dest := e.classify(rec)
	out := &kgo.Record{
		Topic:     dest,
		Partition: rec.Partition,
		Key:       rec.Key,
		Value:     rec.Value,
		Headers:   rec.Headers,
		Timestamp: rec.Timestamp,
	}
	if err := e.produceTxnLocked(cycleCtx, out); err != nil {
		return err
	}
	if err := consumer.CommitRecords(cycleCtx, rec); err != nil {
		return fmt.Errorf("kafka: committing source offset: %w", err)
	}
	return nil
}

// classify returns rec's destination shadow topic: the first installed
// route whose filter matches rec's headers, or the app shadow. The header
// map is built lazily, only once some route has a non-empty filter to test.
func (e *Engine) classify(rec *kgo.Record) string {
	var headers map[string]string
	for _, r := range *e.routes.Load() {
		if len(r.Filter) != 0 && headers == nil {
			headers = headerMap(rec.Headers)
		}
		if engine.FilterMatches(r.Filter, headers) {
			return r.Shadow
		}
	}
	return e.appShadow
}

// produceTxnLocked produces rec inside a single begin/produce/end
// transaction cycle, solely for the producer-epoch fence: exactly-once
// delivery is not promised. The caller must hold producerMu.
func (e *Engine) produceTxnLocked(ctx context.Context, rec *kgo.Record) error {
	if err := e.producer.BeginTransaction(); err != nil {
		return fmt.Errorf("kafka: begin transaction: %w", err)
	}
	res := e.producer.ProduceSync(ctx, rec)
	if err := res.FirstErr(); err != nil {
		_ = e.producer.EndTransaction(ctx, kgo.TryAbort)
		if isFencedProducerErr(err) {
			return fmt.Errorf("kafka: producer fenced: %w", err)
		}
		return fmt.Errorf("kafka: producing to %q: %w", rec.Topic, err)
	}
	if err := e.producer.EndTransaction(ctx, kgo.TryCommit); err != nil {
		if isFencedProducerErr(err) {
			return fmt.Errorf("kafka: producer fenced: %w", err)
		}
		return fmt.Errorf("kafka: committing transaction: %w", err)
	}
	return nil
}

// stopPump signals an orderly shutdown -- so the pump's in-flight
// produce-commit cycle finishes and its exit is not mistaken for a fault --
// then cancels and waits for the pump and ownership-monitor goroutines, if
// running, and closes the consumer client. It is idempotent.
func (e *Engine) stopPump(ctx context.Context) {
	e.mu.Lock()
	pumpCancel := e.pumpCancel
	pumpDone := e.pumpDone
	monCancel := e.monitorCancel
	monDone := e.monitorDone
	e.pumpCancel = nil
	e.monitorCancel = nil
	e.mu.Unlock()

	if pumpCancel != nil {
		e.stopRequested.Store(true)
	}
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
	consumer := e.consumer
	admin := e.admin
	e.consumer = nil
	e.mu.Unlock()

	// Best-effort: release this instance's static membership slot so a
	// later admin offset read/commit or group delete is not blocked by a
	// phantom member. This runs even when this process never built a local
	// consumer -- an Aborting Recover fences the producer and stops here
	// without ever joining the group, yet a crashed predecessor's static
	// slot may still be occupying it.
	if admin != nil {
		_, _ = admin.LeaveGroup(ctx, kadm.LeaveGroup(e.splitterGroup).InstanceIDs(e.instanceID))
	}
	if consumer != nil {
		consumer.Close()
	}
}
