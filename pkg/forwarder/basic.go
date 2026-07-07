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

	// Dialer returns the Dialer injected via WithDialer, or nil when the
	// forwarder dials in its own network namespace.
	Dialer() Dialer
}

type basic struct {
	tag        tunnel.Tag
	target     netip.AddrPort
	listenPort int32
	listener   ListenerFactory
	dialer     Dialer
}

// ListenerFactory creates the listen sockets a forwarder uses. The default
// creates them in the forwarder's own network namespace; alternative
// implementations (e.g. one that enters another namespace) can be injected via
// WithListener so a forwarder binds elsewhere.
type ListenerFactory interface {
	Listen(ctx context.Context, network, address string) (net.Listener, error)
	ListenPacket(ctx context.Context, network, address string) (net.PacketConn, error)
}

// defaultListenerFactory creates listen sockets in the caller's own network
// namespace using net.ListenConfig.
type defaultListenerFactory struct{}

func (defaultListenerFactory) Listen(ctx context.Context, network, address string) (net.Listener, error) {
	lc := net.ListenConfig{}
	return lc.Listen(ctx, network, address)
}

func (defaultListenerFactory) ListenPacket(ctx context.Context, network, address string) (net.PacketConn, error) {
	lc := net.ListenConfig{}
	return lc.ListenPacket(ctx, network, address)
}

// Dialer establishes the outbound connection a forwarder makes to its target.
// When a forwarder has no Dialer it dials in its own network namespace; an
// alternative (e.g. one that enters another namespace) can be injected via
// WithDialer so a forwarder's pass-through traffic originates elsewhere.
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// Option configures a Forwarder at construction time.
type Option func(*basic)

// WithListener injects the ListenerFactory a forwarder uses to create its listen
// sockets. Without this option, a forwarder listens in its own network namespace.
func WithListener(lf ListenerFactory) Option {
	return func(b *basic) {
		b.listener = lf
	}
}

// WithDialer injects the Dialer a forwarder uses for its outbound connection.
// Without this option, a forwarder dials in its own network namespace.
func WithDialer(d Dialer) Option {
	return func(b *basic) {
		b.dialer = d
	}
}

// New creates a TCP or UDP forwarder that will forward connections from the given port to the given target.
func New(from types.PortAndProto, tag tunnel.Tag, target netip.AddrPort, opts ...Option) Forwarder {
	if from.Proto == types.ProtoUDP {
		return NewUDP(from.Port, tag, target, opts...)
	}
	return NewTCP(from.Port, tag, target, opts...)
}

func (f *basic) Tag() tunnel.Tag {
	return f.tag
}

func (f *basic) Target() netip.AddrPort {
	return f.target
}

func (f *basic) Dialer() Dialer {
	return f.dialer
}
