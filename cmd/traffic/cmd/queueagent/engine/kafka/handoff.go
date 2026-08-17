package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/queueagent/engine"
)

// drainApplicationPollInterval bounds how often DrainApplication rechecks
// the app shadow's drain proof.
const drainApplicationPollInterval = 250 * time.Millisecond

// frontier is a Handoff's decoded form: the splitter group's committed
// source offset per partition at the moment Stop was called.
type frontier map[int32]int64

// Stop stops the pump and returns the splitter group's committed source
// offsets as the handoff checkpoint.
func (e *Engine) Stop(ctx context.Context) (engine.Handoff, error) {
	e.stopPump(ctx)

	e.mu.Lock()
	err := e.ensureClients()
	e.mu.Unlock()
	if err != nil {
		return nil, err
	}

	offsets, err := e.admin.FetchOffsetsForTopics(ctx, e.splitterGroup, e.cfg.Source)
	if err != nil {
		return nil, fmt.Errorf("kafka: reading splitter frontier: %w", err)
	}
	fr := frontier{}
	for p, o := range offsets[e.cfg.Source] {
		if o.Err != nil {
			return nil, fmt.Errorf("kafka: splitter frontier partition %d: %w", p, o.Err)
		}
		if o.At < 0 {
			continue
		}
		fr[p] = o.At
	}
	h, err := encodeFrontier(fr)
	if err != nil {
		return nil, err
	}
	e.MarkStopped()
	return h, nil
}

// Abort stops source consumption without a handback. It never commits an
// offset on the original app group and never republishes anything: Kafka's
// source is read-only to the splitter, so every record it already forwarded
// is still on the source too, at the app group's pre-activation position --
// that is the rollback. Idempotent.
func (e *Engine) Abort(ctx context.Context) error {
	e.stopPump(ctx)
	e.MarkAborted()
	return nil
}

// DrainApplication returns nil once the app group's committed offsets on
// the app shadow equal the app shadow's end offsets, bounded by ctx.
func (e *Engine) DrainApplication(ctx context.Context) error {
	e.mu.Lock()
	err := e.ensureClients()
	e.mu.Unlock()
	if err != nil {
		return err
	}

	ticker := time.NewTicker(drainApplicationPollInterval)
	defer ticker.Stop()
	for {
		drained, err := e.appShadowDrained(ctx)
		if err != nil {
			return err
		}
		if drained {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// appShadowDrained reports whether the app group's committed offsets on the
// app shadow have caught up to its last real record on every partition.
func (e *Engine) appShadowDrained(ctx context.Context) (bool, error) {
	final, err := e.appShadowFinalOffsets(ctx)
	if err != nil {
		return false, err
	}
	ready, err := e.topicResidue(ctx, e.appShadow, e.appGroup, final)
	if err != nil {
		return false, err
	}
	return ready == 0, nil
}

// appShadowFinalOffsets returns, per partition, the app shadow's last real
// record offset plus one -- computed once and cached, since nothing writes
// to the app shadow anymore by the time DrainApplication is called.
func (e *Engine) appShadowFinalOffsets(ctx context.Context) (map[int32]int64, error) {
	e.appShadowFinalMu.Lock()
	defer e.appShadowFinalMu.Unlock()
	if e.appShadowFinal != nil {
		return e.appShadowFinal, nil
	}
	final, err := e.finalOffsets(ctx, e.appShadow)
	if err != nil {
		return nil, err
	}
	e.appShadowFinal = final
	return final, nil
}

// finalOffsets returns, per partition, topic's last real record offset plus
// one.
//
// A raw ListEndOffsets reading is not directly comparable to a record-based
// committed offset here: every transactionally produced record shares its
// produce with a trailing control (marker) batch that also consumes a log
// offset but is never delivered as a Record, so a fully caught-up
// committed offset sits exactly one marker short of the raw end for every
// transaction since the last one it covers. This is topic's true,
// record-based end -- comparable to committed -- computed by actually
// observing its last real record.
func (e *Engine) finalOffsets(ctx context.Context, topic string) (map[int32]int64, error) {
	rawEnd, err := e.admin.ListEndOffsets(ctx, topic)
	if err != nil {
		return nil, fmt.Errorf("kafka: listing %q end offsets: %w", topic, err)
	}
	if err := rawEnd.Error(); err != nil {
		return nil, fmt.Errorf("kafka: listing %q end offsets: %w", topic, err)
	}

	final := make(map[int32]int64, len(rawEnd[topic]))
	remaining := map[int32]int64{}
	for p, o := range rawEnd[topic] {
		if o.Offset == 0 {
			final[p] = 0
			continue
		}
		remaining[p] = o.Offset
	}
	if len(remaining) > 0 {
		observed, err := e.observeLastRealOffsets(ctx, topic, remaining)
		if err != nil {
			return nil, err
		}
		for p, at := range observed {
			final[p] = at
		}
	}
	return final, nil
}

// topicResidue returns the number of records on topic, at partition
// positions from final (see finalOffsets), that group's committed offsets
// have not yet caught up to.
func (e *Engine) topicResidue(ctx context.Context, topic, group string, final map[int32]int64) (int, error) {
	committed, err := e.admin.FetchOffsetsForTopics(ctx, group, topic)
	if err != nil {
		return 0, fmt.Errorf("kafka: fetching %q group offsets: %w", group, err)
	}
	ready := 0
	for p, want := range final {
		at := int64(0)
		if c, ok := committed.Lookup(topic, p); ok && c.Err == nil && c.At >= 0 {
			at = c.At
		}
		if at < want {
			ready += int(want - at)
		}
	}
	return ready, nil
}

// observeLastRealOffsets consumes topic from its start until every
// partition in rawEnd has reported a fetch whose LastStableOffset has
// reached that partition's raw end, and returns each partition's last
// real record's offset plus one -- the record-based position a caught-up
// committed offset would sit at.
func (e *Engine) observeLastRealOffsets(ctx context.Context, topic string, rawEnd map[int32]int64) (map[int32]int64, error) {
	opts, err := e.baseClientOpts()
	if err != nil {
		return nil, err
	}
	starts := make(map[int32]kgo.Offset, len(rawEnd))
	for p := range rawEnd {
		starts[p] = kgo.NewOffset().AtStart()
	}
	opts = append(opts,
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{topic: starts}),
		kgo.FetchIsolationLevel(kgo.ReadCommitted()),
	)
	consumer, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("kafka: building app shadow observer: %w", err)
	}
	defer consumer.Close()

	final := make(map[int32]int64, len(rawEnd))
	remaining := make(map[int32]int64, len(rawEnd))
	for p, at := range rawEnd {
		remaining[p] = at
	}
	for len(remaining) > 0 {
		fetches, err := pollFetchesRetrying(ctx, consumer)
		if err != nil {
			return nil, err
		}
		fetches.EachPartition(func(p kgo.FetchTopicPartition) {
			if n := len(p.Records); n > 0 {
				final[p.Partition] = p.Records[n-1].Offset + 1
			}
			if target, ok := remaining[p.Partition]; ok && p.LastStableOffset >= target {
				delete(remaining, p.Partition)
			}
		})
	}
	return final, nil
}

// CommitHandoff sets the original app group's source offsets to h's
// frontier. The broker refuses while the group has a live member.
func (e *Engine) CommitHandoff(ctx context.Context, h engine.Handoff) error {
	fr, err := decodeFrontier(h)
	if err != nil {
		return err
	}

	e.mu.Lock()
	err = e.ensureClients()
	e.mu.Unlock()
	if err != nil {
		return err
	}

	var seed kadm.Offsets
	for p, at := range fr {
		seed.AddOffset(e.cfg.Source, p, at, -1)
	}
	if len(seed) == 0 {
		return nil
	}

	resp, err := e.admin.CommitOffsets(ctx, e.cfg.Group, seed)
	if err != nil {
		if isUnknownMemberErr(err) {
			return handoffRefusedErr(e.cfg.Group, err)
		}
		return fmt.Errorf("kafka: committing handoff offsets: %w", err)
	}
	if err := resp.Error(); err != nil {
		if isUnknownMemberErr(err) {
			return handoffRefusedErr(e.cfg.Group, err)
		}
		return fmt.Errorf("kafka: committing handoff offsets: %w", err)
	}
	return nil
}

// VerifyCleanupReady live-reads broker state, never statistics. Stopped
// requires memberless groups and caught-up app-shadow offsets (else
// NeedsDrainError); Aborted requires memberless only; before either, it
// is a plain error.
func (e *Engine) VerifyCleanupReady(ctx context.Context) error {
	e.mu.Lock()
	err := e.ensureClients()
	e.mu.Unlock()
	if err != nil {
		return err
	}

	switch {
	case e.Stopped():
		return e.verifyStoppedCleanupReady(ctx)
	case e.Aborted():
		return e.verifyMemberless(ctx)
	default:
		return fmt.Errorf("kafka: activation is still active, VerifyCleanupReady runs only after Stop or Abort")
	}
}

// verifyStoppedCleanupReady checks group membership, then residue on the app
// shadow and on every route still in knownRoutes(), app shadow first, in
// bookkeeping order -- a session topic sitting behind its group's committed
// offset would otherwise pass readiness and be deleted with content still
// on it.
func (e *Engine) verifyStoppedCleanupReady(ctx context.Context) error {
	if err := e.verifyMemberless(ctx); err != nil {
		return err
	}

	appExists, err := e.topicExists(ctx, e.appShadow)
	if err != nil {
		return err
	}
	if appExists {
		appFinal, err := e.appShadowFinalOffsets(ctx)
		if err != nil {
			return err
		}
		if err := e.checkTopicDrained(ctx, e.appShadow, e.appGroup, appFinal); err != nil {
			return err
		}
	}

	for _, r := range e.knownRoutes() {
		exists, err := e.topicExists(ctx, r.Shadow)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		final, err := e.finalOffsets(ctx, r.Shadow)
		if err != nil {
			return err
		}
		if err := e.checkTopicDrained(ctx, r.Shadow, e.sessionGroupName(r.ID), final); err != nil {
			return err
		}
	}
	return nil
}

// checkTopicDrained returns a *engine.NeedsDrainError for topic when group's
// committed offsets have not caught up to final.
func (e *Engine) checkTopicDrained(ctx context.Context, topic, group string, final map[int32]int64) error {
	ready, err := e.topicResidue(ctx, topic, group, final)
	if err != nil {
		return err
	}
	if ready > 0 {
		return &engine.NeedsDrainError{Queue: topic, Ready: ready}
	}
	return nil
}

// verifyMemberless requires the app group and every known route's session
// group to report zero members, checked with one DescribeGroups call.
func (e *Engine) verifyMemberless(ctx context.Context) error {
	groups := append([]string{e.appGroup}, e.sessionGroupNames()...)
	described, err := e.admin.DescribeGroups(ctx, groups...)
	if err != nil {
		return fmt.Errorf("kafka: describing consumer groups: %w", err)
	}
	for _, g := range groups {
		n, err := groupMemberCount(described, g)
		if err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("kafka: consumer group %q still has %d active member(s)", g, n)
		}
	}
	return nil
}

// sessionGroupNames returns the known routes' session group names.
func (e *Engine) sessionGroupNames() []string {
	routes := e.knownRoutes()
	names := make([]string, len(routes))
	for i, r := range routes {
		names[i] = e.sessionGroupName(r.ID)
	}
	return names
}

// Cleanup deletes the app shadow, every known session shadow, and every
// group this activation created. It refuses while started but neither
// stopped nor aborted. Stopped or Aborted repeats VerifyCleanupReady's
// checks first; a prepare-only activation (never started) skips that -- the
// pump never consumed the source, so no shadow holds anything the app owns.
// Kafka's deletes are loss-proof once verified, so nothing is retained.
func (e *Engine) Cleanup(ctx context.Context) ([]engine.RetainedResource, error) {
	e.mu.Lock()
	err := e.ensureClients()
	e.mu.Unlock()
	if err != nil {
		return nil, err
	}

	if err := e.CleanupGate(); err != nil {
		return nil, fmt.Errorf("kafka: %w", err)
	}
	switch {
	case e.Stopped():
		if err := e.verifyStoppedCleanupReady(ctx); err != nil {
			return nil, err
		}
	case e.Aborted():
		if err := e.verifyMemberless(ctx); err != nil {
			return nil, err
		}
	default:
		// Never started: the pump never consumed the source, so no shadow
		// holds anything the app owns.
	}

	routes := e.knownRoutes()
	topics := make([]string, 0, 1+len(routes))
	topics = append(topics, e.appShadow)
	for _, r := range routes {
		topics = append(topics, r.Shadow)
	}
	if err := deleteTopics(ctx, e.admin, topics); err != nil {
		return nil, err
	}

	groups := make([]string, 0, 2+2*len(routes))
	for _, r := range routes {
		groups = append(groups, e.sessionGroupName(r.ID), e.drainGroupName(r.ID))
	}
	groups = append(groups, e.splitterGroup, e.appGroup)
	if err := deleteGroups(ctx, e.admin, groups); err != nil {
		return nil, err
	}

	empty := []routeEntry{}
	e.routes.Store(&empty)
	return nil, nil
}

// deleteTopics batch-deletes names in one call. Only an explicit
// unknown-topic result counts as already-deleted; a missing response is an
// unconfirmed deletion and fails.
func deleteTopics(ctx context.Context, admin *kadm.Client, names []string) error {
	resp, err := admin.DeleteTopics(ctx, names...)
	if err != nil {
		return fmt.Errorf("kafka: deleting topics: %w", err)
	}
	for _, name := range names {
		r, ok := resp[name]
		if !ok {
			return fmt.Errorf("kafka: deleting shadow topic %q: no response from broker", name)
		}
		if r.Err != nil && !isUnknownTopicErr(r.Err) {
			return fmt.Errorf("kafka: deleting shadow topic %q: %w", name, r.Err)
		}
	}
	return nil
}

// deleteGroups batch-deletes names in one call. Only an explicit
// unknown-group result counts as already-deleted; a missing response is an
// unconfirmed deletion and fails.
func deleteGroups(ctx context.Context, admin *kadm.Client, names []string) error {
	resp, err := admin.DeleteGroups(ctx, names...)
	if err != nil {
		return fmt.Errorf("kafka: deleting groups: %w", err)
	}
	for _, name := range names {
		r, ok := resp[name]
		if !ok {
			return fmt.Errorf("kafka: deleting group %q: no response from broker", name)
		}
		if r.Err != nil && !isUnknownGroupErr(r.Err) {
			return fmt.Errorf("kafka: deleting group %q: %w", name, r.Err)
		}
	}
	return nil
}

// Recover rebuilds clients and ensures Prepare-level resources exist, then
// acts on st's persisted phase alone -- never inferring it from
// broker-resource existence. st.Started false leaves the source unconsumed.
// st.Started true and st.Aborting true reacquires the producer fence and
// installs st.Routes for the retried Abort and the Cleanup that follows,
// but never resumes the pump. st.Started true, st.Aborting false, and
// st.Stopped false installs st.Routes and resumes the pump. st.Stopped true
// installs st.Routes, for Cleanup's benefit, but never restarts the pump:
// DrainApplication, CommitHandoff, and Cleanup are ready to run from
// st.Handoff alone.
// st.Retained is not restored: kafka's DrainRoute and Cleanup never retain a
// resource, so there is nothing to recover.
func (e *Engine) Recover(ctx context.Context, st engine.RecoveredState) error {
	e.mu.Lock()
	err := e.prepareResourcesLocked(ctx)
	e.mu.Unlock()
	if err != nil {
		return err
	}

	if !st.Started {
		return nil
	}
	e.Restore(st)

	if st.Aborting {
		e.mu.Lock()
		err := e.fenceProducerLocked(ctx)
		e.mu.Unlock()
		if err != nil {
			return err
		}
		return e.installRoutes(ctx, st.Routes)
	}

	if err := e.installRoutes(ctx, st.Routes); err != nil {
		return err
	}
	if st.Stopped {
		return nil
	}
	return e.startPump(ctx)
}

// encodeFrontier JSON-encodes fr as an engine.Handoff.
func encodeFrontier(fr frontier) (engine.Handoff, error) {
	data, err := json.Marshal(fr)
	if err != nil {
		return nil, fmt.Errorf("kafka: encoding handoff: %w", err)
	}
	return engine.Handoff(data), nil
}

// decodeFrontier decodes an engine.Handoff produced by Stop.
func decodeFrontier(h engine.Handoff) (frontier, error) {
	var fr frontier
	if err := json.Unmarshal(h, &fr); err != nil {
		return nil, fmt.Errorf("kafka: decoding handoff: %w", err)
	}
	return fr, nil
}
