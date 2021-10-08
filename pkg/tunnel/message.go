package tunnel

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

type MessageCode byte

const (
	Normal = MessageCode(iota)
	StreamInfo
	StreamOK
	DialOK
	DialReject
	Disconnect
	KeepAlive
	Session
	closeSend
)

func (c MessageCode) String() string {
	switch c {
	case StreamInfo:
		return "STREAM_INFO"
	case StreamOK:
		return "STREAM_OK"
	case DialOK:
		return "DIAL_OK"
	case DialReject:
		return "DIAL_REJECT"
	case Disconnect:
		return "DISCONNECT_OK"
	case KeepAlive:
		return "KEEP_ALIVE"
	case Session:
		return "SESSION"
	case closeSend:
		return "CLOSE_SEND"
	default:
		return fmt.Sprintf("** unknown control code: %d **", c)
	}
}

type Message interface {
	Code() MessageCode
	Payload() []byte
	TunnelMessage() *manager.TunnelMessage
}

type msg []byte

func (c msg) Code() MessageCode {
	return MessageCode(c[0])
}

func (c msg) Payload() []byte {
	return c[1:]
}

func (c msg) String() string {
	code := c.Code()
	if code == Normal {
		return fmt.Sprintf("len %d", len(c.Payload()))
	}
	return fmt.Sprintf("code %s, len %d", code, len(c.Payload()))
}

func (c msg) TunnelMessage() *manager.TunnelMessage {
	return &manager.TunnelMessage{Payload: c}
}

func NewMessage(code MessageCode, payload []byte) Message {
	if pl := len(payload); pl > 0 {
		c := makeMessage(code, len(payload))
		copy(c.Payload(), payload)
		return c
	}
	return msg{byte(code)}
}

func StreamInfoMessage(id ConnID, sessionID string, callDelay, dialTimeout time.Duration) Message {
	b := bytes.Buffer{}
	b.WriteByte(byte(StreamInfo))

	buf := make([]byte, 8)
	n := binary.PutUvarint(buf, uint64(Version))
	b.Write(buf[:n])

	n = binary.PutUvarint(buf, uint64(callDelay))
	b.Write(buf[:n])

	n = binary.PutUvarint(buf, uint64(dialTimeout))
	b.Write(buf[:n])

	idb := []byte(id)
	n = binary.PutUvarint(buf, uint64(len(idb)))
	b.Write(buf[:n])
	b.Write(idb)

	sb := []byte(sessionID)
	n = binary.PutUvarint(buf, uint64(len(sb)))
	b.Write(buf[:n])
	b.Write(sb)
	return msg(b.Bytes())
}

func StreamOKMessage() Message {
	m := makeMessage(StreamOK, 4)
	n := binary.PutUvarint(m.Payload(), uint64(Version))
	return m[:n+1]
}

func SessionMessage(sessionID string) Message {
	return NewMessage(Session, []byte(sessionID))
}

func GetSession(m Message) string {
	return string(m.Payload())
}

func makeMessage(code MessageCode, payloadLength int) msg {
	m := make(msg, 1+payloadLength)
	m[0] = byte(code)
	return m
}

// getVersion returns the version that this Message represents
func getVersion(m Message) uint16 {
	v, _ := binary.Uvarint(m.Payload())
	return uint16(v)
}

var errMalformedConnect = errors.New("malformed Connect message")

// connectInfo returns the connectInfo that this Message represents
func setConnectInfo(m Message, s *stream) error {
	pl := m.Payload()

	v, n := binary.Uvarint(pl)
	if n <= 0 {
		return errMalformedConnect
	}
	s.peerVersion = uint16(v)
	pl = pl[n:]

	v, n = binary.Uvarint(pl)
	if n <= 0 {
		return errMalformedConnect
	}
	s.roundtripLatency = time.Duration(v)
	pl = pl[n:]

	v, n = binary.Uvarint(pl)
	if n <= 0 {
		return errMalformedConnect
	}
	s.dialTimeout = time.Duration(v)
	pl = pl[n:]

	v, n = binary.Uvarint(pl)
	if n <= 0 || v > uint64(len(pl)) {
		return errMalformedConnect
	}
	pl = pl[n:]
	s.id = ConnID(pl[:v])
	pl = pl[v:]

	v, n = binary.Uvarint(pl)
	if n <= 0 || v > uint64(len(pl)) {
		return errMalformedConnect
	}
	pl = pl[n:]
	s.sessionID = string(pl[:v])
	return nil
}
