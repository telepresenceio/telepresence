package tunnel

import (
	"context"
	"io"
	"time"
)

// NewPipe creates a pair of Streams connected using two channels.
func NewPipe(id ConnID, sessionID SessionID, tagA, tagB Tag) (Stream, Stream) {
	out := make(chan Message, 1)
	in := make(chan Message, 1)
	return &channelStream{
			id:     id,
			sid:    sessionID,
			tag:    tagA,
			recvCh: in,
			sendCh: out,
		}, &channelStream{
			id:     id,
			sid:    sessionID,
			tag:    tagB,
			recvCh: out,
			sendCh: in,
		}
}

type channelStream struct {
	id     ConnID
	sid    SessionID
	tag    Tag
	recvCh <-chan Message
	sendCh chan<- Message
}

func (s channelStream) SetTag(_ Tag) {
}

func (s channelStream) Tag() Tag {
	return s.tag
}

func (s channelStream) ID() ConnID {
	return s.id
}

func (s channelStream) Receive(ctx context.Context) (Message, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case m, ok := <-s.recvCh:
		if !ok {
			return nil, io.EOF
		}
		return m, nil
	}
}

func (s channelStream) Send(ctx context.Context, message Message) error {
	select {
	case <-ctx.Done():
	case s.sendCh <- message:
	}
	return nil
}

func (s channelStream) CloseSend(_ context.Context) error {
	close(s.sendCh)
	return nil
}

func (s channelStream) PeerVersion() uint16 {
	return 2
}

func (s channelStream) SessionID() SessionID {
	return s.sid
}

func (s channelStream) DialTimeout() time.Duration {
	return time.Second
}

func (s channelStream) RoundtripLatency() time.Duration {
	return time.Millisecond
}
