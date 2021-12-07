package manager

import (
	"encoding/binary"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

// muxExchangeVersion reads the version of the origin and sends our version back using the
// deprecated mux tunnel
// Deprecated
func muxExchangeVersion(origin string, version uint16, server interface {
	Recv() (*manager.ConnMessage, error)
	Send(*manager.ConnMessage) error
}) error {
	const versionCtl = 9 // Mux tunnel control code for version
	cm, err := server.Recv()
	if err != nil {
		return status.Errorf(codes.FailedPrecondition, "failed to read %s tunnel version: %v", origin, err)
	}
	peerVersion := uint16(0)
	if ctrl := cm.GetConnId(); len(ctrl) == 2 {
		idLen := int(ctrl[1])
		if payload := cm.GetPayload(); len(payload) >= idLen && ctrl[0] == versionCtl {
			peerVersion = binary.BigEndian.Uint16(payload[idLen:])
		}
	}
	if peerVersion < 2 {
		return status.Errorf(codes.Unimplemented, "unsupported %s tunnel version %d. Minimum supported version is 2", origin, peerVersion)
	}

	payload := make([]byte, 2)
	binary.BigEndian.PutUint16(payload, version)
	cm = &manager.ConnMessage{
		ConnId:  []byte{versionCtl, 0},
		Payload: payload,
	}
	if err := server.Send(cm); err != nil {
		return status.Errorf(codes.FailedPrecondition, "failed to send manager tunnel version to %s: %v", origin, err)
	}
	return nil
}

// ClientTunnel just reports that the version of this traffic-manager. Mux tunnels are no longer supported but
// still used by for version exchange by clients prior to 2.5.0
// Deprecated
func (m *Manager) ClientTunnel(server manager.Manager_ClientTunnelServer) error {
	_, err := server.Recv() // client session
	if err != nil {
		return status.Errorf(codes.FailedPrecondition, "failed to read client session info message: %v", err)
	}
	if err = muxExchangeVersion("client", tunnel.Version, server); err != nil {
		return err
	}
	return nil
}

// AgentTunnel just reports that the version of this traffic-manager. Mux tunnels are no longer supported but
// still used by for version exchange by clients prior to 2.5.0
// Deprecated
func (m *Manager) AgentTunnel(server manager.Manager_AgentTunnelServer) error {
	var err error
	if _, err = server.Recv(); err != nil { // agent session
		return status.Errorf(codes.FailedPrecondition, "failed to read agent session info message: %v", err)
	}
	if _, err = server.Recv(); err != nil { // client session
		return status.Errorf(codes.FailedPrecondition, "failed to read client session info message: %v", err)
	}
	if err = muxExchangeVersion("agent", tunnel.Version, server); err != nil {
		return err
	}
	return nil
}
