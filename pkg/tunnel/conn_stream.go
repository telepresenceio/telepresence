package tunnel

import (
	"context"
	"net"
	"time"
)

type connStream struct {
	net.Conn
	id               ConnID
	dialTimeout      time.Duration
	roundtripLatency time.Duration
	sessionID        string
}

func NewConnStream(conn net.Conn, id ConnID, sessionID string, dialTimeout, rountripLatency time.Duration) Stream {
	return &connStream{
		Conn:             conn,
		id:               id,
		dialTimeout:      dialTimeout,
		roundtripLatency: rountripLatency,
		sessionID:        sessionID,
	}
}

func (c *connStream) Tag() string {
	return "FWD"
}

func (c *connStream) ID() ConnID {
	return c.id
}

func (c *connStream) Receive(_ context.Context) (Message, error) {
	var buf [0x10000]byte
	n, err := c.Read(buf[:])
	if err != nil {
		return nil, err
	}
	var dta []byte
	if n > 0 {
		dta = make([]byte, n)
		copy(dta, buf[:n])
	}
	return NewMessage(Normal, dta), nil
}

func (c *connStream) Send(_ context.Context, message Message) error {
	if message.Code() == Normal {
		_, err := c.Write(message.Payload())
		return err
	}
	return nil
}

func (c *connStream) CloseSend(_ context.Context) error {
	return c.Close()
}

func (c *connStream) PeerVersion() uint16 {
	return Version
}

func (c *connStream) SessionID() string {
	return c.sessionID
}

func (c *connStream) DialTimeout() time.Duration {
	return c.dialTimeout
}

func (c *connStream) RoundtripLatency() time.Duration {
	return c.roundtripLatency
}
