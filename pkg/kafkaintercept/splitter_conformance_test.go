package kafkaintercept

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

func TestTransactionalSplitterConformance(t *testing.T) {
	brokers := testKafkaBrokers(t)
	admin := newTestAdmin(t, brokers)
	defer admin.Close()

	name := fmt.Sprintf("tp-split-%d", time.Now().UnixNano())
	source := name + "-source"
	app := name + "-app"
	route := name + "-route"
	createTestTopics(t, admin, 2, source, app, route)

	producer, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.RecordPartitioner(kgo.ManualPartitioner()),
		kgo.TransactionalID(name+"-source-txn"),
	)
	require.NoError(t, err)
	defer producer.Close()

	wantApp := make([]string, 0, 4)
	wantRoute := make([]string, 0, 4)
	require.NoError(t, producer.BeginTransaction())
	for partition := int32(0); partition < 2; partition++ {
		for index := 0; index < 4; index++ {
			value := fmt.Sprintf("p%d-%d", partition, index)
			tenant := "green"
			if index%2 == 0 {
				tenant = "blue"
				wantRoute = append(wantRoute, value)
			} else {
				wantApp = append(wantApp, value)
			}
			result := producer.ProduceSync(t.Context(), &kgo.Record{
				Topic: source, Partition: partition, Key: []byte(value), Value: []byte(value),
				Headers:   []kgo.RecordHeader{{Key: "tenant", Value: []byte(tenant)}},
				Timestamp: time.Unix(1_700_000_000+int64(index), 0),
			})
			require.NoError(t, result.FirstErr())
		}
	}
	require.NoError(t, producer.EndTransaction(t.Context(), kgo.TryCommit))
	require.NoError(t, producer.BeginTransaction())
	require.NoError(t, producer.ProduceSync(t.Context(), &kgo.Record{
		Topic: source, Partition: 0, Key: []byte("aborted"), Value: []byte("aborted"),
	}).FirstErr())
	require.NoError(t, producer.EndTransaction(t.Context(), kgo.TryAbort))
	require.NoError(t, producer.BeginTransaction())
	require.NoError(t, producer.ProduceSync(t.Context(), &kgo.Record{
		Topic: source, Partition: 0, Key: []byte("after-abort"), Value: []byte("after-abort"),
	}).FirstErr())
	require.NoError(t, producer.EndTransaction(t.Context(), kgo.TryCommit))
	wantApp = append(wantApp, "after-abort")
	plainProducer, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.RecordPartitioner(kgo.ManualPartitioner()))
	require.NoError(t, err)
	for partition := int32(0); partition < 2; partition++ {
		value := fmt.Sprintf("final-p%d", partition)
		require.NoError(t, plainProducer.ProduceSync(t.Context(), &kgo.Record{
			Topic: source, Partition: partition, Key: []byte(value), Value: []byte(value),
		}).FirstErr())
		wantApp = append(wantApp, value)
	}
	plainProducer.Close()

	group := name + "-group"
	splitter, err := NewSplitter(SplitterConfig{
		Brokers: brokers, Group: group, InstanceID: name + "-0", TransactionalID: name + "-txn-0",
		AppTopics: map[string]string{source: app}, OffsetReset: "earliest", BatchSize: 3,
		InitialRoutes: RoutingTable{Generation: 1, Routes: []Route{{
			ID: "blue", Predicate: Predicate{Headers: map[string][]byte{"tenant": []byte("blue")}},
			Topics: map[string]string{source: route},
		}}},
	})
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(t.Context())
	runDone := make(chan error, 1)
	go func() { runDone <- splitter.Run(runCtx) }()

	waitForGroupAtEnd(t, admin, group, source)
	cancel()
	require.NoError(t, <-runDone)

	gotApp := readCommittedValues(t, brokers, app, len(wantApp))
	gotRoute := readCommittedValues(t, brokers, route, len(wantRoute))
	sort.Strings(wantApp)
	sort.Strings(wantRoute)
	sort.Strings(gotApp)
	sort.Strings(gotRoute)
	assert.Equal(t, wantApp, gotApp)
	assert.Equal(t, wantRoute, gotRoute)
}

func TestTransactionalSplitterRebalancesAcrossReplicas(t *testing.T) {
	brokers := testKafkaBrokers(t)
	admin := newTestAdmin(t, brokers)
	defer admin.Close()

	name := fmt.Sprintf("tp-rebalance-%d", time.Now().UnixNano())
	source := name + "-source"
	app := name + "-app"
	createTestTopics(t, admin, 4, source, app)
	group := name + "-group"

	start := func(ordinal int) (context.CancelFunc, <-chan error) {
		splitter, err := NewSplitter(SplitterConfig{
			Brokers: brokers, Group: group,
			InstanceID:      fmt.Sprintf("%s-%d", name, ordinal),
			TransactionalID: fmt.Sprintf("%s-txn-%d", name, ordinal),
			AppTopics:       map[string]string{source: app}, OffsetReset: "earliest", BatchSize: 1,
		})
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- splitter.Run(ctx) }()
		return cancel, done
	}

	producer, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.RecordPartitioner(kgo.ManualPartitioner()))
	require.NoError(t, err)
	defer producer.Close()
	produceWave := func(wave int) []string {
		values := make([]string, 0, 4)
		for partition := int32(0); partition < 4; partition++ {
			value := fmt.Sprintf("wave-%d-partition-%d", wave, partition)
			values = append(values, value)
			require.NoError(t, producer.ProduceSync(t.Context(), &kgo.Record{
				Topic: source, Partition: partition, Key: []byte(value), Value: []byte(value),
			}).FirstErr())
		}
		return values
	}

	cancel0, done0 := start(0)
	waitForGroupMembers(t, admin, group, 1)
	want := produceWave(1)
	waitForGroupAtEnd(t, admin, group, source)

	cancel1, done1 := start(1)
	waitForGroupMembers(t, admin, group, 2)
	want = append(want, produceWave(2)...)
	waitForGroupAtEnd(t, admin, group, source)

	cancel0()
	require.NoError(t, <-done0)
	waitForGroupMembers(t, admin, group, 1)
	want = append(want, produceWave(3)...)
	waitForGroupAtEnd(t, admin, group, source)
	cancel1()
	require.NoError(t, <-done1)

	got := readCommittedValues(t, brokers, app, len(want))
	sort.Strings(want)
	sort.Strings(got)
	assert.Equal(t, want, got)
}

func TestTransactionalSplitterRecoversOpenTransaction(t *testing.T) {
	brokers := testKafkaBrokers(t)
	admin := newTestAdmin(t, brokers)
	defer admin.Close()

	name := fmt.Sprintf("tp-crash-%d", time.Now().UnixNano())
	source := name + "-source"
	app := name + "-app"
	createTestTopics(t, admin, 1, source, app)

	plainProducer, err := kgo.NewClient(kgo.SeedBrokers(brokers...))
	require.NoError(t, err)
	require.NoError(t, plainProducer.ProduceSync(t.Context(), &kgo.Record{
		Topic: source, Key: []byte("one"), Value: []byte("one"),
	}).FirstErr())
	plainProducer.Close()

	transactionalID := name + "-txn-0"
	stale, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.TransactionalID(transactionalID),
		kgo.RecordPartitioner(kgo.ManualPartitioner()),
	)
	require.NoError(t, err)
	require.NoError(t, stale.BeginTransaction())
	require.NoError(t, stale.ProduceSync(t.Context(), &kgo.Record{
		Topic: app, Partition: 0, Key: []byte("one"), Value: []byte("one"),
	}).FirstErr())
	stale.Close()

	group := name + "-group"
	splitter, err := NewSplitter(SplitterConfig{
		Brokers: brokers, Group: group, InstanceID: name + "-0", TransactionalID: transactionalID,
		AppTopics: map[string]string{source: app}, OffsetReset: "earliest", BatchSize: 1,
	})
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(t.Context())
	runDone := make(chan error, 1)
	go func() { runDone <- splitter.Run(runCtx) }()
	waitForGroupAtEnd(t, admin, group, source)
	cancel()
	require.NoError(t, <-runDone)

	assert.Equal(t, []string{"one"}, readCommittedValues(t, brokers, app, 1))
}

func testKafkaBrokers(t *testing.T) []string {
	t.Helper()
	raw := os.Getenv("TP_TEST_KAFKA_BROKERS")
	if raw == "" {
		t.Skip("TP_TEST_KAFKA_BROKERS is not set")
	}
	return strings.Split(raw, ",")
}

func newTestAdmin(t *testing.T, brokers []string) *kadm.Client {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		admin, err := kadm.NewOptClient(kgo.SeedBrokers(brokers...))
		if err == nil {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			_, lastErr = admin.BrokerMetadata(ctx)
			cancel()
			if lastErr == nil {
				return admin
			}
			admin.Close()
		} else {
			lastErr = err
		}
		time.Sleep(time.Second)
	}
	require.NoError(t, lastErr)
	return nil
}

func createTestTopics(t *testing.T, admin *kadm.Client, partitions int32, topics ...string) {
	t.Helper()
	for _, topic := range topics {
		response, err := admin.CreateTopic(t.Context(), partitions, 1, nil, topic)
		require.NoError(t, err)
		require.NoError(t, response.Err)
	}
}

func waitForGroupAtEnd(t *testing.T, admin *kadm.Client, group string, topics ...string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	var detail string
	for time.Now().Before(deadline) {
		ends, endErr := admin.ListEndOffsets(t.Context(), topics...)
		committed, commitErr := admin.FetchOffsetsForTopics(t.Context(), group, topics...)
		if endErr == nil && commitErr == nil {
			atEnd := true
			for _, topic := range topics {
				for partition, end := range ends[topic] {
					offset, ok := committed.Lookup(topic, partition)
					if !ok || offset.Err != nil || offset.At != end.Offset {
						atEnd = false
						detail = fmt.Sprintf("%s[%d]: committed=%d end=%d", topic, partition, offset.At, end.Offset)
						break
					}
				}
			}
			if atEnd {
				return
			}
		} else {
			detail = fmt.Sprintf("endErr=%v commitErr=%v", endErr, commitErr)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("group %q did not reach topic ends: %s", group, detail)
}

func waitForGroupMembers(t *testing.T, admin *kadm.Client, group string, count int) {
	t.Helper()
	require.Eventually(t, func() bool {
		groups, err := admin.DescribeGroups(t.Context(), group)
		if err != nil {
			return false
		}
		described, ok := groups[group]
		return ok && described.Err == nil && described.State == "Stable" && len(described.Members) == count
	}, time.Minute, 100*time.Millisecond, "group %q did not stabilize with %d members", group, count)
}

func readCommittedValues(t *testing.T, brokers []string, topic string, count int) []string {
	t.Helper()
	metadataClient, err := kgo.NewClient(kgo.SeedBrokers(brokers...))
	require.NoError(t, err)
	admin := kadm.NewClient(metadataClient)
	metadata, err := admin.ListEndOffsets(t.Context(), topic)
	require.NoError(t, err)
	assignments := make(map[int32]kgo.Offset, len(metadata[topic]))
	for partition := range metadata[topic] {
		assignments[partition] = kgo.NewOffset().AtStart()
	}
	admin.Close()

	consumer, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{topic: assignments}),
		kgo.FetchIsolationLevel(kgo.ReadCommitted()),
	)
	require.NoError(t, err)
	defer consumer.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	values := make([]string, 0, count)
	for len(values) < count {
		fetches := consumer.PollRecords(ctx, count-len(values))
		require.NoError(t, fetches.Err())
		for _, record := range fetches.Records() {
			values = append(values, string(record.Value))
		}
	}
	return values
}
