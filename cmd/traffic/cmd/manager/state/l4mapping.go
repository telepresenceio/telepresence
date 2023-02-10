package state

import (
	"fmt"
	"net"

	"github.com/telepresenceio/telepresence/v2/pkg/ipproto"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

type l3Mapping struct {
	tunnel.ConnID
	active bool
}

// newMapping creates a mapping from an srcIP to a destIP. The mapping will map all ports transparently
// except the one port that it is created with. That port is explicitly mapped to another port.
//
// Example:
//
//	Let's say a map is created with 10.20.30.40:54387 => 10.20.30.62:21. An access to 10.20.30.40:54387
//	will then get redirected to 10.20.30.62:21, i.e. both the IP and the port will change. An access to
//	10.20.30.40:1234 however, will only change the IP and redirect to 10.20.30.62:1234.
//
// The reason for this behavior is that FTP, when using PASV and EPSV, will requuest new ports from the
// server. The server replies with a port number, and the client then connects to that port. So while
// mapping the original IP and port used by FTP is fine, all subsequent calls must map the IP only.
//
// Isn't there a risk that the explicit port collides with other ports then? Well, yes, there is, so
// this type of mapping must be used with caution. The client session will only use this mechanism
// to map the initial ftp or sftp address, and will always use the default ports (21 and 22) as the
// source in the mapping. The traffic-agents ftp and sftp servers will never use ports < 1000 so
// it should be quite safe.
func newMapping(proto int, srcIP, destIP net.IP, srcPort, destPort uint16) *l3Mapping {
	return &l3Mapping{
		ConnID: tunnel.NewConnID(proto, srcIP, destIP, srcPort, destPort),
		active: true,
	}
}

// mappedID returns its argument if the destination of the given id doesn't match the source of this
// mapping. When there's a mach, the mapped ConnID is return, or an empty ConnID if is inactive.
func (m *l3Mapping) mappedID(id tunnel.ConnID) (tunnel.ConnID, bool) {
	if !(m.Protocol() == id.Protocol() && id.Destination().Equal(m.Source())) {
		return id, false
	}
	if !m.active {
		return "", true
	}
	var targetPort uint16
	if id.DestinationPort() == m.SourcePort() {
		// Use explicit port mapping
		targetPort = m.DestinationPort()
	} else {
		// Map IP but let port remain as is. This ensures that FTP PASV works OK.
		targetPort = id.DestinationPort()
	}
	return tunnel.NewConnID(m.Protocol(), id.Source(), m.Destination(), id.SourcePort(), targetPort), true
}

func (m *l3Mapping) setActive(active bool) {
	m.active = active
}

func (m *l3Mapping) setDestination(destIP net.IP, destPort uint16) {
	m.ConnID = tunnel.NewConnID(m.Protocol(), m.Source(), destIP, m.SourcePort(), destPort)
	m.active = true
}

func (m *l3Mapping) String() string {
	return fmt.Sprintf("%s mapping %s -> %s",
		ipproto.String(m.Protocol()),
		iputil.JoinIpPort(m.Source(), m.SourcePort()),
		iputil.JoinIpPort(m.Destination(), m.DestinationPort()))
}
