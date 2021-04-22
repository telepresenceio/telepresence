package connpool

import (
	"errors"
	"fmt"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

type ControlCode byte

const (
	SessionID = ControlCode(iota)
	Connect
	ConnectOK
	ConnectReject
	Disconnect
	DisconnectOK
	ReadClosed
	WriteClosed
)

func (c ControlCode) String() string {
	switch c {
	case SessionID:
		return "SESSION_ID"
	case Connect:
		return "CONNECT"
	case ConnectOK:
		return "CONNECT_OK"
	case ConnectReject:
		return "CONNECT_REJECT"
	case Disconnect:
		return "DISCONNECT"
	case DisconnectOK:
		return "DISCONNECT_OK"
	case ReadClosed:
		return "READ_CLOSED"
	case WriteClosed:
		return "WRITE_CLOSED"
	default:
		return fmt.Sprintf("** unknown control code: %d **", c)
	}
}

type ControlMessage struct {
	Code    ControlCode
	ID      ConnID
	Payload []byte
}

func (c *ControlMessage) String() string {
	return fmt.Sprintf("%s, code %s, len %d", c.ID, c.Code, len(c.Payload))
}

func IsControlMessage(cm *manager.ConnMessage) bool {
	return len(cm.ConnId) == 2
}

func ConnControl(id ConnID, code ControlCode, payload []byte) *manager.ConnMessage {
	idLen := len(id)
	cmPl := make([]byte, idLen+len(payload))
	copy(cmPl, id)
	copy(cmPl[idLen:], payload)
	return &manager.ConnMessage{ConnId: []byte{byte(code), byte(idLen)}, Payload: cmPl}
}

func NewControlMessage(cm *manager.ConnMessage) (*ControlMessage, error) {
	ctrl := cm.ConnId
	if len(ctrl) == 2 {
		idLen := int(ctrl[1])
		if len(cm.Payload) >= idLen {
			return &ControlMessage{Code: ControlCode(ctrl[0]), ID: ConnID(cm.Payload[:idLen]), Payload: cm.Payload[idLen:]}, nil
		}
	}
	return nil, errors.New("invalid tunnel control message")
}
