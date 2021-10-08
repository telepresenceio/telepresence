package connpool

import (
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

// Message a message on the multiplexed tunnel
// Deprecated
type Message interface {
	ID() tunnel.ConnID
	Payload() []byte
	TunnelMessage() *manager.ConnMessage
}

// message implements Message
// Deprecated
type message struct {
	msg *manager.ConnMessage
}

// NewMessage
// Deprecated
func NewMessage(id tunnel.ConnID, payload []byte) Message {
	return &message{msg: &manager.ConnMessage{ConnId: []byte(id), Payload: payload}}
}

// FromConnMessage
// Deprecated
func FromConnMessage(cm *manager.ConnMessage) Message {
	ctrl := cm.GetConnId()
	if len(ctrl) == 2 {
		idLen := int(ctrl[1])
		payload := cm.GetPayload()
		if len(payload) >= idLen {
			return NewControl(tunnel.ConnID(cm.Payload[:idLen]), ControlCode(ctrl[0]), payload[idLen:])
		}
	}
	return &message{msg: cm}
}

func (c *message) ID() tunnel.ConnID {
	return tunnel.ConnID(c.msg.GetConnId())
}

func (c *message) Payload() []byte {
	return c.msg.GetPayload()
}

func (c *message) TunnelMessage() *manager.ConnMessage {
	return c.msg
}
