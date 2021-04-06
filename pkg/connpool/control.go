package connpool

import (
	"errors"
	"fmt"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

type ControlCode byte

const (
	Connect = ControlCode(iota)
	ConnectOK
	ConnectReject
	Disconnect
	DisconnectOK
	ReadClosed
	WriteClosed
)

func (c ControlCode) String() string {
	switch c {
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
	ID   ConnID
	Code ControlCode
}

func (c *ControlMessage) String() string {
	return fmt.Sprintf("%s, code %s", c.ID, c.Code)
}

func IsControlMessage(cm *manager.ConnMessage) bool {
	return len(cm.ConnId) == 2
}

func ConnControl(id ConnID, code ControlCode) *manager.ConnMessage {
	// Propagate the error in a "close" message using a datagram where the destination port is set to zero
	// and instead propagated as the first two bytes of the payload followed by an error code.
	ctrl := make([]byte, 2)
	ctrl[0] = byte(code)
	ctrl[1] = byte(len(id))
	return &manager.ConnMessage{
		ConnId:  ctrl,
		Payload: []byte(id),
	}
}

func NewControlMessage(cm *manager.ConnMessage) (*ControlMessage, error) {
	ctrl := cm.ConnId
	if len(ctrl) != 2 || len(cm.Payload) < int(ctrl[1]) {
		return nil, errors.New("invalid tunnel control message")
	}

	return &ControlMessage{
		ID:   ConnID(cm.Payload[:ctrl[1]]),
		Code: ControlCode(ctrl[0]),
	}, nil
}
