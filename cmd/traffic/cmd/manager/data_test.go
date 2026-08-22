package manager

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func TestValidateKafkaIntercept(t *testing.T) {
	valid := &rpc.InterceptSpec{
		Client: "developer", Agent: "checkout", Namespace: "shop", Mechanism: "tcp",
		Kafka: &rpc.KafkaIntercept{Headers: []*rpc.KafkaHeader{{Name: "tenant", Value: []byte("blue")}}},
	}
	require.Empty(t, validateIntercept(valid))

	invalid := proto.Clone(valid).(*rpc.InterceptSpec)
	invalid.Kafka = &rpc.KafkaIntercept{Key: []byte("one"), KeyPrefix: []byte("two")}
	require.Contains(t, validateIntercept(invalid), "both key and key prefix")

	invalid = proto.Clone(valid).(*rpc.InterceptSpec)
	invalid.Kafka = &rpc.KafkaIntercept{Only: true}
	invalid.PortIdentifier = "http"
	require.Contains(t, validateIntercept(invalid), "cannot request network")
}
