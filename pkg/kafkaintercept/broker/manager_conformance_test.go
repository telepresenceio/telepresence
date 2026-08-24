package broker

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/api/v1alpha1"
)

func TestManagedShadowsAndTransactionalDrain(t *testing.T) {
	brokers := testKafkaBrokers(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	client, err := kgo.NewClient(kgo.SeedBrokers(brokers...))
	require.NoError(t, err)
	defer client.Close()
	admin := kadm.NewClient(client)
	suffix := time.Now().Format("150405.000000")
	source := "tp.broker.source." + suffix
	responses, err := admin.CreateTopics(ctx, 2, 1, nil, source)
	require.NoError(t, err)
	require.NoError(t, responses.Error())
	require.Eventually(t, func() bool {
		topics, err := admin.ListTopics(ctx, source)
		detail, ok := topics[source]
		return err == nil && ok && detail.Err == nil && len(detail.Partitions) == 2
	}, 30*time.Second, 100*time.Millisecond)
	t.Cleanup(func() { _, _ = admin.DeleteTopics(context.Background(), source) })

	scheme := runtime.NewScheme()
	require.NoError(t, api.AddToScheme(scheme))
	reader := fake.NewClientBuilder().WithScheme(scheme).Build()
	replication := int32(1)
	split := &api.KafkaSplit{
		ObjectMeta: metav1.ObjectMeta{Name: "orders", Namespace: "shop", UID: types.UID("split-" + suffix)},
		Spec: api.KafkaSplitSpec{
			Connection: api.KafkaConnectionSpec{BootstrapServers: brokers},
			Source:     api.KafkaSourceSpec{Group: "tp.broker.source.group." + suffix, Topics: []string{source}, OffsetReset: "earliest"},
			Shadows: api.KafkaShadowSpec{Mode: api.ShadowModeManaged, Managed: &api.KafkaManagedShadows{
				ReplicationFactor: &replication,
			}},
		},
	}
	manager, err := Open(ctx, reader, split.Namespace, split.Spec.Connection)
	require.NoError(t, err)
	defer manager.Close()
	prepared, err := manager.Prepare(ctx, split)
	require.NoError(t, err)
	preparedAgain, err := manager.Prepare(ctx, split)
	require.NoError(t, err)
	require.Equal(t, prepared.ApplicationTopics, preparedAgain.ApplicationTopics)

	route := &api.KafkaRoute{ObjectMeta: metav1.ObjectMeta{Name: "alice"}}
	session, err := manager.EnsureSession(ctx, split, route)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = manager.DeleteManaged(context.Background(), append(session.Resources, prepared.Resources...))
	})
	timestamp := time.Now().Add(-time.Minute).Truncate(time.Millisecond)
	record := &kgo.Record{
		Topic: session.Topics[source], Partition: 1, Key: []byte("tenant-a"), Value: []byte("payload"),
		Headers: []kgo.RecordHeader{{Key: "tenant", Value: []byte("a")}}, Timestamp: timestamp,
	}
	producer, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.TransactionalID("tp.broker.test."+suffix))
	require.NoError(t, err)
	require.NoError(t, producer.BeginTransaction())
	require.NoError(t, producer.ProduceSync(ctx, record).FirstErr())
	require.NoError(t, producer.EndTransaction(ctx, kgo.TryCommit))
	producer.Close()
	require.NoError(t, manager.DrainSession(ctx, split, route.Name, session.Group, session.Topics, prepared.ApplicationTopics))
	remaining, err := manager.Remaining(ctx, session.Group, session.Topics)
	require.NoError(t, err)
	require.Zero(t, remaining)

	consumer, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...), kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{
			prepared.ApplicationTopics[source]: {1: kgo.NewOffset().AtStart()},
		}), kgo.FetchIsolationLevel(kgo.ReadCommitted()),
	)
	require.NoError(t, err)
	defer consumer.Close()
	fetches := consumer.PollRecords(ctx, 1)
	require.NoError(t, fetches.Err())
	require.Len(t, fetches.Records(), 1)
	actual := fetches.Records()[0]
	require.Equal(t, record.Key, actual.Key)
	require.Equal(t, record.Value, actual.Value)
	require.Equal(t, record.Headers, actual.Headers)
	require.Equal(t, timestamp, actual.Timestamp)

	for _, topic := range session.Topics {
		responses, err := admin.DeleteTopics(ctx, topic)
		require.NoError(t, err)
		require.NoError(t, responses.Error())
		require.Eventually(t, func() bool {
			details, err := admin.ListTopics(ctx, topic)
			detail, ok := details[topic]
			return err == nil && (!ok || errors.Is(detail.Err, kerr.UnknownTopicOrPartition))
		}, 30*time.Second, 100*time.Millisecond)
	}
	// A retry after topic deletion must still converge on the remaining
	// group deletion rather than treating the missing topic as an error.
	require.NoError(t, manager.DeleteManaged(ctx, session.Resources))
	require.NoError(t, manager.DeleteManaged(ctx, session.Resources))
}

func testKafkaBrokers(t *testing.T) []string {
	t.Helper()
	raw := os.Getenv("TP_TEST_KAFKA_BROKERS")
	if raw == "" {
		t.Skip("TP_TEST_KAFKA_BROKERS is not set")
	}
	return strings.Split(raw, ",")
}
