package tunnel

import (
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// Version
//   0 which didn't report versions and didn't do synchronization
//   1 used MuxTunnel instead of one tunnel per connection.
const Version = uint16(2)

// GRPCStream is the bare minimum needed for reading and writing TunnelMessages
// on a Manager_TunnelServer or Manager_TunnelClient.
type GRPCStream interface {
	Recv() (*rpc.TunnelMessage, error)
	Send(*rpc.TunnelMessage) error
}
