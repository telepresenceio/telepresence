package connpool

import (
	"encoding/binary"
	"encoding/json"
	"fmt"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

type ControlCode byte

const (
	SessionInfo = ControlCode(iota)
	Connect
	ConnectOK
	ConnectReject
	Disconnect
	DisconnectOK
	ReadClosed  // deprecated, treat as Disconnect
	WriteClosed // deprecated, treat as Disconnect
	KeepAlive
	version
	syncRequest
	syncResponse
)

func (c ControlCode) String() string {
	switch c {
	case SessionInfo:
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
	case KeepAlive:
		return "KEEP_ALIVE"
	case version:
		return "VERSION"
	case syncRequest:
		return "SYNC_REQUEST"
	case syncResponse:
		return "SYNC_RESPONSE"
	default:
		return fmt.Sprintf("** unknown control code: %d **", c)
	}
}

type Control interface {
	Message
	Code() ControlCode
	SessionInfo() *manager.SessionInfo
	ackNumber() uint32
	version() uint16
}

type control struct {
	code    ControlCode
	id      ConnID
	payload []byte
}

func (c *control) Code() ControlCode {
	return c.code
}

func (c *control) ID() ConnID {
	return c.id
}

func (c *control) Payload() []byte {
	return c.payload
}

// AckNumber returns the AckNumber that this Control represents or zero if
// this isn't a syncResponse Control.
func (c *control) ackNumber() uint32 {
	if c.code == syncResponse {
		return binary.BigEndian.Uint32(c.payload)
	}
	return 0
}

// SessionInfo returns the SessionInfo that this Control represents or nil if
// this isn't a SessionInfo Control.
func (c *control) SessionInfo() *manager.SessionInfo {
	if c.code == SessionInfo {
		var sessionInfo *manager.SessionInfo
		if err := json.Unmarshal(c.payload, &sessionInfo); err == nil {
			return sessionInfo
		}
	}
	return nil
}

func (c *control) String() string {
	return fmt.Sprintf("%s, code %s, len %d", c.id, c.code, len(c.payload))
}

func (c *control) TunnelMessage() *manager.ConnMessage {
	idLen := len(c.id)
	cmPl := make([]byte, idLen+len(c.payload))
	copy(cmPl, c.id)
	copy(cmPl[idLen:], c.payload)
	return &manager.ConnMessage{ConnId: []byte{byte(c.code), byte(idLen)}, Payload: cmPl}
}

func NewControl(id ConnID, code ControlCode, payload []byte) Control {
	return &control{id: id, code: code, payload: payload}
}

func SessionInfoControl(sessionInfo *manager.SessionInfo) Control {
	jsonInfo, err := json.Marshal(sessionInfo)
	if err != nil {
		// The SessionInfo must be json Marshable
		panic(err)
	}
	return &control{id: "", code: SessionInfo, payload: jsonInfo}
}

func SyncRequestControl(ackNbr uint32) Control {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, ackNbr)
	// Need a ZeroID here to prevent older managers and agents from crashing.
	return &control{id: NewZeroID(), code: syncRequest, payload: payload}
}

func SyncResponseControl(request Control) Control {
	return &control{id: request.ID(), code: syncResponse, payload: request.Payload()}
}

func VersionControl() Control {
	payload := make([]byte, 2)
	binary.BigEndian.PutUint16(payload, tunnelVersion)
	return &control{id: NewZeroID(), code: version, payload: payload}
}

// version returns the tunnel version that this Control represents or zero if
// this isn't a version Control.
func (c *control) version() uint16 {
	if c.code == version {
		return binary.BigEndian.Uint16(c.payload)
	}
	return 0
}
