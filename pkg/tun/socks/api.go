package socks

import (
	"context"
	"net"
)

type Client interface {
	NewDialer(ctx context.Context, network string, proxyPort uint16) (Dialer, error)
}

type Dialer interface {
	DialContext(ctx context.Context, from net.IP, fromPort uint16, to net.IP, toPort uint16) (net.Conn, error)

	ProxyPort() uint16
}
