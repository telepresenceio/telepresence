package tunnel

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

type scriptedStream struct {
	msgs []*manager.TunnelMessage
}

func (s *scriptedStream) Recv() (*manager.TunnelMessage, error) {
	if len(s.msgs) == 0 {
		return nil, io.EOF
	}
	m := s.msgs[0]
	s.msgs = s.msgs[1:]
	return m, nil
}

func (s *scriptedStream) Send(*manager.TunnelMessage) error { return nil }

func TestNewServerStream_RejectsBadFirstMessage(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
	}{
		{"nil payload", nil},
		{"empty payload", []byte{}},
		{"unknown code", []byte{0xff}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gs := &scriptedStream{msgs: []*manager.TunnelMessage{{Payload: tt.payload}}}
			require.NotPanics(t, func() {
				_, err := NewServerStream(context.Background(), ClientToManager, gs)
				require.Error(t, err)
			})
		})
	}
}
