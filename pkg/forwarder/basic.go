package forwarder

import (
	"context"
	"net"
	"net/netip"

	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// A Forwarder forwards TCP or UDP connections.
type Forwarder interface {
	// Forward from the given client connection to the target host:port of this forwarder.
	// The client connection will be closed after the forwarding is complete.
	Forward(ctx context.Context, clientConn net.Conn) error

	// Listen returns a listener that will listen for connections to the port specified in the forwarder and
	// updates the port in the forwarder in case the port was initially zero.
	// Listen is only implemented for TCP.
	Listen(ctx context.Context) (net.Listener, error)

	// ListenPort returns the port that this forwarder will listen to. This port will be updated
	// by a call to Serve if the port was initially zero.
	ListenPort() types.PortAndProto

	// Serve will call the ServeTCP or ServeUDP method depending on the protocol of the forwarder.
	Serve(ctx context.Context, initCh chan<- netip.AddrPort) error

	// ServeTo starts the listener and accept-loop for this forwarder. The listener will listen to all available addresses on the given port.
	// The accept-loop calls the fw function a separate go-routine for each accepted connection.
	// The fw function is responsible for closing the connection.
	// The port can be zero, in which case the listener will assign a random port number.
	// The port number can be retrieved via the ListenPort method.
	ServeTo(ctx context.Context, initCh chan<- netip.AddrPort, fw func(context.Context, net.Conn) error) error

	// Tag used for logging purposes.
	Tag() tunnel.Tag

	// Target returns the target host:port that this forwarder forwards to.
	Target() netip.AddrPort
}

type basic struct {
	tag        tunnel.Tag
	target     netip.AddrPort
	listenPort int32
}

// New creates a TCP or UDP forwarder that will forward connections from the given port to the given target.
func New(from types.PortAndProto, tag tunnel.Tag, target netip.AddrPort) Forwarder {
	if from.Proto == types.ProtoUDP {
		return NewUDP(from.Port, tag, target)
	}
	return NewTCP(from.Port, tag, target)
}

func (f *basic) Tag() tunnel.Tag {
	return f.tag
}

func (f *basic) Target() netip.AddrPort {
	return f.target
}
