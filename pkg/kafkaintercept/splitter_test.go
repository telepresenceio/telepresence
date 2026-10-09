package kafkaintercept

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
)

func validSplitterConfig() SplitterConfig {
	return SplitterConfig{
		Brokers:            []string{"broker:9092"},
		Group:              "orders",
		InstanceID:         "splitter-0",
		TransactionalID:    "splitter-txn-0",
		AppTopics:          map[string]string{"orders": "orders-app"},
		OffsetReset:        "earliest",
		BatchSize:          10,
		TransactionTimeout: 30 * time.Second,
	}
}

func TestRoutingTableValidation(t *testing.T) {
	appTopics := map[string]string{"orders": "orders-app"}
	require.NoError(t, validateRoutingTable(RoutingTable{Routes: []Route{
		{ID: "blue", Predicate: Predicate{Headers: map[string][]byte{"tenant": []byte("blue")}}, Topics: map[string]string{"orders": "orders-blue"}},
		{ID: "green", Predicate: Predicate{Headers: map[string][]byte{"tenant": []byte("green")}}, Topics: map[string]string{"orders": "orders-green"}},
	}}, appTopics))
	require.Error(t, validateRoutingTable(RoutingTable{Routes: []Route{
		{ID: "west", Predicate: Predicate{Headers: map[string][]byte{"region": []byte("west")}}, Topics: map[string]string{"orders": "orders-west"}},
		{ID: "blue", Predicate: Predicate{Headers: map[string][]byte{"tenant": []byte("blue")}}, Topics: map[string]string{"orders": "orders-blue"}},
	}}, appTopics))
	require.Error(t, validateRoutingTable(RoutingTable{Routes: []Route{
		{ID: "missing", Topics: map[string]string{}},
	}}, appTopics))
}

func TestSplitterConfigValidation(t *testing.T) {
	cfg := validSplitterConfig()
	require.NoError(t, validateSplitterConfig(cfg))
	cfg.TransactionalID = ""
	require.Error(t, validateSplitterConfig(cfg))
}

func TestClassifyFetchErrors(t *testing.T) {
	var logged int
	logf := func(string, ...any) { logged++ }

	require.NoError(t, classifyFetchErrors(nil, logf))

	require.NoError(t, classifyFetchErrors([]kgo.FetchError{
		{Topic: "orders", Partition: 0, Err: kerr.NotLeaderForPartition},
		{Topic: "orders", Partition: 1, Err: context.DeadlineExceeded},
	}, logf))
	require.Equal(t, 2, logged)

	err := classifyFetchErrors([]kgo.FetchError{
		{Topic: "orders", Partition: 0, Err: kerr.NotLeaderForPartition},
		{Topic: "orders", Partition: 2, Err: kerr.InvalidTopicException},
	}, logf)
	require.ErrorIs(t, err, kerr.InvalidTopicException)
}

func TestCloneRoutingTableCopiesMutableData(t *testing.T) {
	original := RoutingTable{Routes: []Route{{
		ID: "blue",
		Predicate: Predicate{
			Headers: map[string][]byte{"tenant": []byte("blue")},
			Key:     []byte("key"),
		},
		Topics: map[string]string{"orders": "orders-blue"},
	}}}
	cloned := cloneRoutingTable(original)
	original.Routes[0].Predicate.Headers["tenant"][0] = 'X'
	original.Routes[0].Predicate.Key[0] = 'X'
	original.Routes[0].Topics["orders"] = "changed"
	require.Equal(t, []byte("blue"), cloned.Routes[0].Predicate.Headers["tenant"])
	require.Equal(t, []byte("key"), cloned.Routes[0].Predicate.Key)
	require.Equal(t, "orders-blue", cloned.Routes[0].Topics["orders"])
}
