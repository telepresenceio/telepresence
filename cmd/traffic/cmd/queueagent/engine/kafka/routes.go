package kafka

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/queueagent/engine"
	"github.com/telepresenceio/telepresence/v2/pkg/queuestate"
)

// drainPollInterval bounds how often DrainRoute polls the drain group's
// progress toward the captured end offsets.
const drainPollInterval = 100 * time.Millisecond

// sessionQuiescePollInterval bounds how often DrainRoute polls the session
// group's membership while waiting for a live developer consumer to leave.
const sessionQuiescePollInterval = 250 * time.Millisecond

// ReconcileRoutes installs routes as the pump's classification table,
// creating a session shadow topic for each route not previously seen.
func (e *Engine) ReconcileRoutes(ctx context.Context, routes []engine.Route) error {
	e.mu.Lock()
	err := e.ensureClients()
	e.mu.Unlock()
	if err != nil {
		return err
	}
	return e.installRoutes(ctx, routes)
}

// installRoutes creates a session shadow topic for each route and installs
// routes as the pump's route table in one atomic swap.
func (e *Engine) installRoutes(ctx context.Context, routes []engine.Route) error {
	entries := make([]routeEntry, 0, len(routes))
	for _, r := range routes {
		shadow := e.sessionShadowName(r.ID)
		if err := e.ensureShadowTopic(ctx, shadow); err != nil {
			return err
		}
		entries = append(entries, routeEntry{ID: r.ID, Filter: r.Filter, Shadow: shadow})
	}
	e.routes.Store(&entries)
	return nil
}

func (e *Engine) sessionShadowName(routeID string) string {
	return queuestate.SessionShadowName(e.cfg.InstallID, e.cfg.WorkloadUID, e.cfg.QueueName, e.cfg.ActivationID, routeID)
}

func (e *Engine) sessionGroupName(routeID string) string {
	return queuestate.SessionGroupName(e.cfg.InstallID, e.cfg.WorkloadUID, e.cfg.QueueName, e.cfg.ActivationID, routeID)
}

func (e *Engine) drainGroupName(routeID string) string {
	return queuestate.DrainGroupName(e.cfg.InstallID, e.cfg.WorkloadUID, e.cfg.QueueName, e.cfg.ActivationID, routeID)
}

// removeRoute drops id from the pump's route table so no further record
// classifies to it.
func (e *Engine) removeRoute(id string) {
	old := *e.routes.Load()
	next := make([]routeEntry, 0, len(old))
	for _, r := range old {
		if r.ID != id {
			next = append(next, r)
		}
	}
	e.routes.Store(&next)
}

// DrainRoute stops classifying messages to id, waits for the publish
// barrier, waits for the session group to have no live members, moves the
// session shadow's unconsumed suffix to the app shadow, and deletes the
// session shadow topic and its session/drain groups once the move reaches
// the captured end offsets. Group deletion always runs, even when the topic
// is already gone from an earlier attempt, so a retry that finds the topic
// absent still cleans up a group a prior attempt failed to delete instead of
// reporting success while leaking it. Kafka's deletes are loss-proof once
// verified, so nothing is ever retained.
func (e *Engine) DrainRoute(ctx context.Context, id string) ([]engine.RetainedResource, error) {
	e.mu.Lock()
	err := e.ensureClients()
	e.mu.Unlock()
	if err != nil {
		return nil, err
	}

	e.removeRoute(id)

	// Publish barrier: wait for any produce already selected under the
	// old table (still targeting this route's shadow) to be acknowledged.
	e.producerMu.Lock()
	e.producerMu.Unlock() //nolint:staticcheck // barrier: acquire-then-release proves quiescence

	shadow := e.sessionShadowName(id)
	sessionGroup := e.sessionGroupName(id)
	drainGroup := e.drainGroupName(id)

	exists, err := e.topicExists(ctx, shadow)
	if err != nil {
		return nil, err
	}
	if exists {
		// A live developer consumer under sessionGroup would otherwise
		// process the same suffix the drain group is about to move,
		// delivering it to both that consumer and the app.
		if err := e.waitSessionGroupQuiesced(ctx, sessionGroup); err != nil {
			return nil, err
		}
		if _, err := e.moveRouteResidue(ctx, shadow, sessionGroup, drainGroup); err != nil {
			return nil, err
		}
		if _, err := e.admin.DeleteTopic(ctx, shadow); err != nil && !isUnknownTopicErr(err) {
			return nil, fmt.Errorf("kafka: deleting session shadow %q: %w", shadow, err)
		}
	}
	for _, g := range []string{sessionGroup, drainGroup} {
		if _, err := e.admin.DeleteGroup(ctx, g); err != nil && !isUnknownGroupErr(err) {
			return nil, fmt.Errorf("kafka: deleting group %q: %w", g, err)
		}
	}
	return nil, nil
}

// waitSessionGroupQuiesced blocks, bounded by ctx, until group has zero
// members.
func (e *Engine) waitSessionGroupQuiesced(ctx context.Context, group string) error {
	for {
		n, err := describeGroupMembers(ctx, e.admin, group)
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sessionQuiescePollInterval):
		}
	}
}

// moveRouteResidue drains shadow's unconsumed suffix -- the portion after
// sessionGroup's committed position -- into the app shadow using drainGroup,
// committing each drain offset only after its produce is acknowledged. It
// returns the per-partition end offsets it drained up to.
func (e *Engine) moveRouteResidue(ctx context.Context, shadow, sessionGroup, drainGroup string) (map[int32]int64, error) {
	sessionOffsets, err := e.admin.FetchOffsetsForTopics(ctx, sessionGroup, shadow)
	if err != nil {
		return nil, fmt.Errorf("kafka: fetching session group offsets: %w", err)
	}
	endOffsets, err := e.admin.ListEndOffsets(ctx, shadow)
	if err != nil {
		return nil, fmt.Errorf("kafka: listing session shadow end offsets: %w", err)
	}
	if err := endOffsets.Error(); err != nil {
		return nil, fmt.Errorf("kafka: listing session shadow end offsets: %w", err)
	}

	target := make(map[int32]int64, len(endOffsets[shadow]))
	remaining := map[int32]bool{}
	for p, o := range endOffsets[shadow] {
		target[p] = o.Offset
		start := int64(0)
		if so, ok := sessionOffsets.Lookup(shadow, p); ok && so.Err == nil && so.At >= 0 {
			start = so.At
		}
		if start < o.Offset {
			remaining[p] = true
		}
	}
	if len(remaining) == 0 {
		return target, nil
	}

	if err := e.seedDrainGroup(ctx, drainGroup, shadow, sessionOffsets); err != nil {
		return nil, err
	}

	if err := e.drainToAppShadow(ctx, shadow, drainGroup, target, remaining); err != nil {
		return nil, err
	}
	return target, nil
}

// seedDrainGroup seeds drainGroup's offsets from sessionOffsets for every
// partition it does not already have a committed offset for, so a retried
// DrainRoute resumes drain progress instead of restarting it.
func (e *Engine) seedDrainGroup(ctx context.Context, drainGroup, shadow string, sessionOffsets kadm.OffsetResponses) error {
	current, err := e.admin.FetchOffsetsForTopics(ctx, drainGroup, shadow)
	if err != nil {
		return fmt.Errorf("kafka: fetching drain group offsets: %w", err)
	}

	var seed kadm.Offsets
	for p, o := range current[shadow] {
		if o.Err == nil && o.At >= 0 {
			continue // already seeded
		}
		start := int64(0)
		if so, ok := sessionOffsets.Lookup(shadow, p); ok && so.Err == nil && so.At >= 0 {
			start = so.At
		}
		seed.AddOffset(shadow, p, start, -1)
	}
	if len(seed) == 0 {
		return nil
	}
	resp, err := e.admin.CommitOffsets(ctx, drainGroup, seed)
	if err != nil {
		return fmt.Errorf("kafka: seeding drain group %q: %w", drainGroup, err)
	}
	return resp.Error()
}

// drainToAppShadow consumes shadow as drainGroup until every partition in
// remaining has been consumed up to target, producing each record to the
// app shadow and committing its drain offset only after the produce is
// acknowledged.
func (e *Engine) drainToAppShadow(
	ctx context.Context, shadow, drainGroup string, target map[int32]int64, remaining map[int32]bool,
) error {
	opts, err := e.baseClientOpts()
	if err != nil {
		return err
	}
	opts = append(opts,
		kgo.ConsumerGroup(drainGroup),
		kgo.ConsumeTopics(shadow),
		kgo.DisableAutoCommit(),
		kgo.FetchIsolationLevel(kgo.ReadCommitted()),
	)
	consumer, err := kgo.NewClient(opts...)
	if err != nil {
		return fmt.Errorf("kafka: building drain consumer: %w", err)
	}
	defer consumer.Close()

	for len(remaining) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		fetches := consumer.PollFetches(ctx)
		if err := fetches.Err(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			time.Sleep(drainPollInterval)
			continue
		}

		var drainErr error
		fetches.EachPartition(func(p kgo.FetchTopicPartition) {
			if drainErr != nil {
				return
			}
			for _, rec := range p.Records {
				if err := e.drainOneRecord(ctx, consumer, rec); err != nil {
					drainErr = err
					return
				}
			}
			// Every record in a transaction shares its produce with a
			// trailing control (marker) batch that also consumes a log
			// offset but is never delivered as a Record, so a record's
			// own offset+1 can never reach target when the source used
			// transactional produces. LastStableOffset is a raw log
			// position, directly comparable to target, and already
			// reflects every decided transaction as of this fetch.
			if p.LastStableOffset >= target[p.Partition] {
				delete(remaining, p.Partition)
			}
		})
		if drainErr != nil {
			return drainErr
		}
	}
	return nil
}

// drainOneRecord produces rec to the app shadow and commits its drain
// offset only once the produce is acknowledged.
func (e *Engine) drainOneRecord(ctx context.Context, consumer *kgo.Client, rec *kgo.Record) error {
	e.producerMu.Lock()
	defer e.producerMu.Unlock()

	out := &kgo.Record{
		Topic:     e.appShadow,
		Partition: rec.Partition,
		Key:       rec.Key,
		Value:     rec.Value,
		Headers:   rec.Headers,
		Timestamp: rec.Timestamp,
	}
	if err := e.produceTxnLocked(ctx, out); err != nil {
		return err
	}
	if err := consumer.CommitRecords(ctx, rec); err != nil {
		return fmt.Errorf("kafka: committing drain offset: %w", err)
	}
	return nil
}

// knownRoutes returns a sorted snapshot of the currently installed routes.
func (e *Engine) knownRoutes() []routeEntry {
	entries := slices.Clone(*e.routes.Load())
	slices.SortFunc(entries, func(a, b routeEntry) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return entries
}
