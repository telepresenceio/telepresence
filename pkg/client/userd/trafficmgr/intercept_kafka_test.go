package trafficmgr

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func TestPrepareKafkaIntercept(t *testing.T) {
	tests := []struct {
		name      string
		kafka     *manager.KafkaIntercept
		supported bool
		wantOnly  bool
		wantKafka bool
		wantError string
	}{
		{name: "not requested"},
		{name: "supported discovery", kafka: &manager.KafkaIntercept{}, supported: true, wantKafka: true},
		{name: "supported Kafka only", kafka: &manager.KafkaIntercept{Only: true}, supported: true, wantOnly: true, wantKafka: true},
		{name: "old manager strips discovery", kafka: &manager.KafkaIntercept{}},
		{name: "old manager rejects explicit routing", kafka: &manager.KafkaIntercept{Key: []byte("blue")}, wantKafka: true, wantError: "does not support"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := &manager.InterceptSpec{Kafka: tt.kafka}
			only, err := prepareKafkaIntercept(spec, tt.supported)
			if tt.wantError != "" {
				require.ErrorContains(t, err, tt.wantError)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.wantOnly, only)
			require.Equal(t, tt.wantKafka, spec.Kafka != nil)
		})
	}
}
