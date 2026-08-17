package kafka

import (
	"context"
	"fmt"
	"strings"

	"github.com/twmb/franz-go/pkg/kadm"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/queueagent/engine"
)

// unboundedRetention is the topic config every durable shadow this engine
// creates uses: delete cleanup with retention disabled, so a shadow holds
// everything published to it until this engine deletes the topic.
func unboundedRetention() map[string]*string {
	infinite := "-1"
	return map[string]*string{
		"retention.ms":    &infinite,
		"retention.bytes": &infinite,
	}
}

// Prepare creates the app shadow topic, without consuming the source. The
// splitter group's starting offset is not seeded here: that happens in
// Start, at the moment pumping is actually about to begin, from the app
// group's position at that later moment -- not whatever it was at Prepare
// time, which may be long before Start if a restart intervenes.
func (e *Engine) Prepare(ctx context.Context) (engine.EnvOverrides, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.prepareResourcesLocked(ctx); err != nil {
		return nil, err
	}
	return engine.EnvOverrides{
		e.cfg.SourceEnvName: e.appShadow,
		e.cfg.GroupEnvName:  e.appGroup,
	}, nil
}

// prepareResourcesLocked creates the app shadow topic, idempotently. The
// caller must hold mu.
func (e *Engine) prepareResourcesLocked(ctx context.Context) error {
	if err := e.ensureClients(); err != nil {
		return err
	}
	partitions, err := e.sourcePartitionCount(ctx)
	if err != nil {
		return err
	}
	return e.ensureShadowTopic(ctx, e.appShadow, partitions)
}

// sourcePartitionCount returns the source topic's partition count.
func (e *Engine) sourcePartitionCount(ctx context.Context) (int32, error) {
	topics, err := e.admin.ListTopics(ctx, e.cfg.Source)
	if err != nil {
		return 0, fmt.Errorf("kafka: listing source topic %q: %w", e.cfg.Source, err)
	}
	td, ok := topics[e.cfg.Source]
	if !ok {
		return 0, fmt.Errorf("kafka: source topic %q not found", e.cfg.Source)
	}
	if td.Err != nil {
		return 0, fmt.Errorf("kafka: source topic %q: %w", e.cfg.Source, td.Err)
	}
	return int32(len(td.Partitions)), nil
}

// ensureShadowTopic creates name with partitions partitions and unbounded
// retention, tolerating a topic that already exists, and verifies -- fresh
// create or pre-existing -- that its effective retention is actually
// unbounded: a shadow a broker default or an external override could expire
// would silently lose messages this engine promises to keep.
func (e *Engine) ensureShadowTopic(ctx context.Context, name string, partitions int32) error {
	_, err := e.admin.CreateTopic(ctx, partitions, -1, unboundedRetention(), name)
	if err != nil && !isTopicAlreadyExistsErr(err) {
		return fmt.Errorf("kafka: creating shadow topic %q: %w", name, err)
	}
	return e.verifyUnboundedRetention(ctx, name)
}

// verifyUnboundedRetention rejects name unless its effective retention.ms
// and retention.bytes are both -1 (unbounded). DescribeConfigs reports the
// effective value directly -- already merged from any dynamic override down
// to the broker default -- so this catches a shadow this engine did not
// itself create with the intended config, not just a failed CreateTopic.
func (e *Engine) verifyUnboundedRetention(ctx context.Context, name string) error {
	configs, err := e.admin.DescribeTopicConfigs(ctx, name)
	if err != nil {
		return fmt.Errorf("kafka: describing topic %q configs: %w", name, err)
	}
	rc, err := configs.On(name, nil)
	if err != nil {
		return fmt.Errorf("kafka: describing topic %q configs: %w", name, err)
	}
	for _, key := range [...]string{"retention.ms", "retention.bytes"} {
		found := false
		for _, c := range rc.Configs {
			if c.Key != key {
				continue
			}
			found = true
			if v := c.MaybeValue(); v != "-1" {
				return fmt.Errorf("kafka: shadow topic %q has bounded effective %s=%q, want -1 (unbounded)", name, key, v)
			}
		}
		if !found {
			return fmt.Errorf("kafka: shadow topic %q is missing effective config %s", name, key)
		}
	}
	return nil
}

// topicExists reports whether name currently exists.
func (e *Engine) topicExists(ctx context.Context, name string) (bool, error) {
	topics, err := e.admin.ListTopics(ctx, name)
	if err != nil {
		return false, fmt.Errorf("kafka: listing topic %q: %w", name, err)
	}
	td, ok := topics[name]
	if !ok {
		return false, nil
	}
	if td.Err != nil {
		if isUnknownTopicErr(td.Err) {
			return false, nil
		}
		return false, fmt.Errorf("kafka: describing topic %q: %w", name, td.Err)
	}
	return true, nil
}

// seedSplitterOffsets seeds, per source partition, the splitter group's
// committed offset from the original app group's committed offset, applying
// OffsetReset for a partition neither group has committed. A partition the
// splitter group already has a committed offset for is left untouched, so
// this is safe to call again after the splitter group has made progress.
func (e *Engine) seedSplitterOffsets(ctx context.Context) error {
	current, err := e.admin.FetchOffsetsForTopics(ctx, e.splitterGroup, e.cfg.Source)
	if err != nil {
		return fmt.Errorf("kafka: fetching splitter group offsets: %w", err)
	}

	var missing []int32
	for p, o := range current[e.cfg.Source] {
		if o.Err != nil || o.At < 0 {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		return nil
	}

	appOffsets, err := e.admin.FetchOffsetsForTopics(ctx, e.cfg.Group, e.cfg.Source)
	if err != nil {
		return fmt.Errorf("kafka: fetching app group offsets: %w", err)
	}

	var resets map[int32]int64
	var seed kadm.Offsets
	for _, p := range missing {
		if ao, ok := appOffsets.Lookup(e.cfg.Source, p); ok && ao.Err == nil && ao.At >= 0 {
			seed.AddOffset(e.cfg.Source, p, ao.At, -1)
			continue
		}
		if resets == nil {
			resets, err = e.resolveOffsetResets(ctx)
			if err != nil {
				return err
			}
		}
		at, ok := resets[p]
		if !ok {
			return fmt.Errorf("kafka: no offset-reset position for source partition %d", p)
		}
		seed.AddOffset(e.cfg.Source, p, at, -1)
	}

	resp, err := e.admin.CommitOffsets(ctx, e.splitterGroup, seed)
	if err != nil {
		if isUnknownMemberErr(err) {
			return fmt.Errorf("kafka: seeding splitter group %q: group has a live member: %w", e.splitterGroup, err)
		}
		return fmt.Errorf("kafka: seeding splitter group offsets: %w", err)
	}
	if err := resp.Error(); err != nil {
		if isUnknownMemberErr(err) {
			return fmt.Errorf("kafka: seeding splitter group %q: group has a live member: %w", e.splitterGroup, err)
		}
		return fmt.Errorf("kafka: seeding splitter group offsets: %w", err)
	}
	return nil
}

// resolveOffsetResets lists, per source partition, the offset cfg.OffsetReset
// names: the log start offset for "earliest", the log end offset for
// "latest".
func (e *Engine) resolveOffsetResets(ctx context.Context) (map[int32]int64, error) {
	var (
		listed kadm.ListedOffsets
		err    error
	)
	switch strings.ToLower(e.cfg.OffsetReset) {
	case "", "earliest":
		listed, err = e.admin.ListStartOffsets(ctx, e.cfg.Source)
	case "latest":
		listed, err = e.admin.ListEndOffsets(ctx, e.cfg.Source)
	default:
		return nil, fmt.Errorf("kafka: unsupported offsetReset %q", e.cfg.OffsetReset)
	}
	if err != nil {
		return nil, fmt.Errorf("kafka: listing offset-reset positions: %w", err)
	}
	if err := listed.Error(); err != nil {
		return nil, fmt.Errorf("kafka: listing offset-reset positions: %w", err)
	}
	resets := make(map[int32]int64, len(listed[e.cfg.Source]))
	for p, o := range listed[e.cfg.Source] {
		resets[p] = o.Offset
	}
	return resets, nil
}
