package kafka

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/queueagent/engine"
	"github.com/telepresenceio/telepresence/v2/pkg/queuestate"
)

// TestDrainBlockedWhileSessionConsumerLive proves DrainRoute's session-group
// quiescence wait: it must not start moving a route's residue while a
// developer's own consumer still holds the session group -- doing so would
// deliver the same suffix to both that consumer and the app.
func TestDrainBlockedWhileSessionConsumerLive(t *testing.T) {
	if len(brokers) == 0 {
		t.Skip("TP_TEST_KAFKA_BROKERS not set")
	}
	ctx := t.Context()

	ts := time.Now().UnixNano()
	source := fmt.Sprintf("tp-drainblock-src-%d", ts)
	admCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	_, err := admin.CreateTopic(admCtx, 1, -1, nil, source)
	cancel()
	require.NoError(t, err)

	cfg := Config{
		InstallID:     "kafka-drain-block",
		WorkloadUID:   fmt.Sprintf("wl-%d", ts),
		QueueName:     "orders",
		ActivationID:  fmt.Sprintf("act-%d", ts),
		Brokers:       brokers,
		Source:        source,
		Group:         fmt.Sprintf("tp-drainblock-appgrp-%d", ts),
		OffsetReset:   "earliest",
		SourceEnvName: "ORDERS_TOPIC",
		GroupEnvName:  "ORDERS_GROUP",
	}
	eng := New(cfg)
	t.Cleanup(func() { _ = eng.Close() })

	_, err = eng.Prepare(ctx)
	require.NoError(t, err)
	require.NoError(t, eng.Start(ctx))

	route := engine.Route{ID: "route-drain-block", Filter: map[string]string{"user": "drain-block"}}
	require.NoError(t, eng.ReconcileRoutes(ctx, []engine.Route{route}))

	shadow := queuestate.SessionShadowName(cfg.InstallID, cfg.WorkloadUID, cfg.QueueName, cfg.ActivationID, route.ID)
	sessionGroup := queuestate.SessionGroupName(cfg.InstallID, cfg.WorkloadUID, cfg.QueueName, cfg.ActivationID, route.ID)

	const n = 3
	ids := make([]string, n)
	for i := range n {
		id := fmt.Sprintf("drain-block-msg-%d", i+1)
		ids[i] = id
		rec := &kgo.Record{
			Topic:   source,
			Key:     []byte(id),
			Value:   []byte(id),
			Headers: toKgoHeaders(map[string]string{"user": "drain-block"}),
		}
		res := producer.ProduceSync(ctx, rec)
		require.NoError(t, res.FirstErr())
	}

	// Disabled autocommit keeps the session group's committed offset at its
	// seed regardless of what this consumer fetches, so the whole batch
	// remains residue for the drain that follows -- matching a developer
	// consumer that has not yet processed what it received.
	consumer, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumerGroup(sessionGroup),
		kgo.ConsumeTopics(shadow),
		kgo.FetchIsolationLevel(kgo.ReadCommitted()),
		kgo.DisableAutoCommit(),
	)
	require.NoError(t, err)
	// One poll joins the group; the client's background heartbeats keep the
	// membership live afterward without further polls.
	joinCtx, joinCancel := context.WithTimeout(ctx, 10*time.Second)
	consumer.PollFetches(joinCtx)
	joinCancel()

	shortCtx, shortCancel := context.WithTimeout(ctx, 4*time.Second)
	_, err = eng.DrainRoute(shortCtx, route.ID)
	shortCancel()
	require.Error(t, err, "DrainRoute must fail while the session consumer is live")

	consumer.Close()

	_, err = eng.DrainRoute(ctx, route.ID)
	require.NoError(t, err)

	appShadow := queuestate.AppShadowName(cfg.InstallID, cfg.WorkloadUID, cfg.QueueName, cfg.ActivationID)
	got := drainBlockCollectShadow(t, ctx, appShadow, len(ids))
	require.ElementsMatch(t, ids, got)

	require.NoError(t, eng.Abort(ctx)) // stops the pump so Cleanup's group deletion isn't blocked by a live member
	_, err = eng.Cleanup(ctx)
	require.NoError(t, err)
}

// drainBlockCollectShadow reads want records from shadow using a fresh
// observer group, returning their IDs.
func drainBlockCollectShadow(t *testing.T, ctx context.Context, shadow string, want int) []string {
	t.Helper()

	cl, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumerGroup("tp-drainblock-observer-"+shadow),
		kgo.ConsumeTopics(shadow),
		kgo.FetchIsolationLevel(kgo.ReadCommitted()),
	)
	require.NoError(t, err)
	defer cl.Close()

	readCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var ids []string
	for len(ids) < want {
		fetches := cl.PollRecords(readCtx, want-len(ids))
		if readCtx.Err() != nil {
			break
		}
		if err := fetches.Err(); err != nil {
			time.Sleep(pollRetryBackoff)
			continue
		}
		for _, r := range fetches.Records() {
			ids = append(ids, string(r.Key))
		}
	}
	return ids
}
