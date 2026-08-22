package kafkaintercept

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
