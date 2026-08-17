package kafka

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/queueagent/engine"
)

// TestCleanupRetriesAfterSessionTopicDeletion recreates the broker state left
// by a Cleanup whose session-topic delete succeeded but whose following group
// delete failed. The retry must treat the missing topic as resolved and still
// delete both remaining groups.
func TestCleanupRetriesAfterSessionTopicDeletion(t *testing.T) {
	if len(brokers) == 0 {
		t.Skip("TP_TEST_KAFKA_BROKERS not set")
	}

	suffix := time.Now().UnixNano()
	eng := newStoppedCleanupTestEngine(t, suffix, []engine.Route{{ID: "route-cleanup-retry"}})
	ctx := t.Context()

	route := engine.Route{ID: "route-cleanup-retry"}
	shadow := eng.sessionShadowName(route.ID)
	groups := []string{eng.sessionGroupName(route.ID), eng.drainGroupName(route.ID)}
	for _, group := range groups {
		var offsets kadm.Offsets
		offsets.AddOffset(shadow, 0, 0, -1)
		resp, err := admin.CommitOffsets(ctx, group, offsets)
		require.NoError(t, err)
		require.NoError(t, resp.Error())
	}

	_, err := admin.DeleteTopic(ctx, shadow)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		exists, existsErr := eng.topicExists(ctx, shadow)
		return existsErr == nil && !exists
	}, 30*time.Second, 100*time.Millisecond)

	_, err = eng.Cleanup(ctx)
	require.NoError(t, err)
	requireGroupsDeleted(t, groups)
}

// TestCleanupRetriesAfterAppTopicDeletion verifies that a retry bypasses the
// app shadow's cached final offsets once a prior Cleanup deleted that topic.
func TestCleanupRetriesAfterAppTopicDeletion(t *testing.T) {
	if len(brokers) == 0 {
		t.Skip("TP_TEST_KAFKA_BROKERS not set")
	}

	suffix := time.Now().UnixNano()
	eng := newStoppedCleanupTestEngine(t, suffix, nil)
	ctx := t.Context()

	result := producer.ProduceSync(ctx, &kgo.Record{
		Topic:     eng.appShadow,
		Partition: 0,
		Key:       []byte("cleanup-retry"),
		Value:     []byte("cached-final-offset"),
	})
	require.NoError(t, result.FirstErr())

	var offsets kadm.Offsets
	offsets.AddOffset(eng.appShadow, 0, result[0].Record.Offset+1, -1)
	committed, err := admin.CommitOffsets(ctx, eng.appGroup, offsets)
	require.NoError(t, err)
	require.NoError(t, committed.Error())
	require.NoError(t, eng.VerifyCleanupReady(ctx))

	_, err = admin.DeleteTopic(ctx, eng.appShadow)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		exists, existsErr := eng.topicExists(ctx, eng.appShadow)
		return existsErr == nil && !exists
	}, 30*time.Second, 100*time.Millisecond)
	_, err = admin.DeleteGroup(ctx, eng.appGroup)
	require.True(t, err == nil || isUnknownGroupErr(err), "deleting app group: %v", err)

	_, err = eng.Cleanup(ctx)
	require.NoError(t, err)
}

func newStoppedCleanupTestEngine(t *testing.T, suffix int64, routes []engine.Route) *Engine {
	t.Helper()
	cfg := Config{
		InstallID:     "kafka-cleanup-retry",
		WorkloadUID:   fmt.Sprintf("wl-%d", suffix),
		QueueName:     "orders",
		ActivationID:  fmt.Sprintf("act-%d", suffix),
		Brokers:       brokers,
		Source:        sourceTopic,
		Group:         fmt.Sprintf("app-%d", suffix),
		OffsetReset:   "latest",
		SourceEnvName: "ORDERS_TOPIC",
		GroupEnvName:  "ORDERS_GROUP",
	}
	eng := New(cfg)
	t.Cleanup(func() { _ = eng.Close() })
	require.NoError(t, eng.Recover(t.Context(), engine.RecoveredState{Started: true, Stopped: true, Routes: routes}))
	return eng
}

func requireGroupsDeleted(t *testing.T, groups []string) {
	t.Helper()
	described, err := admin.DescribeGroups(t.Context(), groups...)
	require.NoError(t, err)
	for _, group := range groups {
		got := described[group]
		require.True(t, isUnknownGroupErr(got.Err) || got.State == "Dead",
			"group %q still exists: state=%q err=%v", group, got.State, got.Err)
	}
}
