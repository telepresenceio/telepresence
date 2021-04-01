package socks

import (
	"context"
	"errors"
	"fmt"
	"net"

	"golang.org/x/net/proxy"
)

type proxyClient struct{}

var Proxy Client = proxyClient{}

func (c proxyClient) NewDialer(ctx context.Context, network string, proxyPort uint16) (Dialer, error) {
	switch network {
	case "tcp", "tcp4", "tcp6":
		return tcpOverSocks5(network, proxyPort)
	default:
		return nil, fmt.Errorf("network %q is not supported by the proxy dialer", network)
	}
}

func tcpOverSocks5(network string, proxyPort uint16) (Dialer, error) {
	dialer, err := proxy.SOCKS5("tcp", fmt.Sprintf("127.0.0.1:%d", proxyPort), nil, proxy.Direct)
	if err != nil {
		return nil, err
	}
	ctxDialer, ok := dialer.(proxy.ContextDialer)
	if !ok {
		return nil, errors.New("proxy.Dialer is not proxy.ContextDialer")
	}
	return &tcpDialer{ContextDialer: ctxDialer, network: network, proxyPort: proxyPort}, nil
}

type tcpDialer struct {
	proxy.ContextDialer
	network   string
	proxyPort uint16
}

func (t *tcpDialer) ProxyPort() uint16 {
	return t.proxyPort
}

func (t *tcpDialer) DialContext(ctx context.Context, _ net.IP, _ uint16, host net.IP, port uint16) (net.Conn, error) {
	return t.ContextDialer.DialContext(ctx, t.network, fmt.Sprintf("%s:%d", host, port))
}
