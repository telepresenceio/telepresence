package tunnel

import (
	"testing"
	"time"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// A cluster test cannot make an agent send a dial request with a bad timing value, so this is a unit test.
func TestValidDialTiming(t *testing.T) {
	tests := []struct {
		name      string
		roundtrip int64
		timeout   int64
		want      bool
	}{
		{"zero", 0, 0, true},
		{"usual", int64(50 * time.Millisecond), int64(5 * time.Second), true},
		{"negative latency", -1, 0, false},
		{"negative timeout", 0, -1, false},
		{"huge latency", 1 << 56, 0, false},
		{"huge timeout", 0, 1 << 62, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dr := &rpc.DialRequest{RoundtripLatency: tt.roundtrip, DialTimeout: tt.timeout}
			if got := validDialTiming(dr); got != tt.want {
				t.Errorf("validDialTiming = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestStreamInfoMessageExtremeTimings(t *testing.T) {
	for _, d := range []time.Duration{-1, 1 << 56, 1<<63 - 1} {
		if m := StreamInfoMessage("tcp:10.0.0.1:1->10.0.0.2:2", "s", d, d); m == nil {
			t.Errorf("no message for %d", d)
		}
	}
}
