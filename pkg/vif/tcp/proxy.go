package tcp

import (
	"net"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

type proxy struct {
	buf  [0x10000]byte
	conn *net.TCPConn
}

func NewProxy(conn *net.TCPConn) tunnel.GRPClientCStream {
	return &proxy{conn: conn}
}

func (p proxy) Recv() (*manager.TunnelMessage, error) {
	n, err := p.conn.Read(p.buf[:])
	if err != nil {
		return nil, err
	}
	return tunnel.NewMessage(tunnel.Normal, p.buf[0:n]).TunnelMessage(), nil
}

func (p proxy) Send(m *manager.TunnelMessage) error {
	_, err := p.conn.Write(m.GetPayload())
	return err
}

func (p proxy) CloseSend() error {
	return p.conn.CloseRead()
}
