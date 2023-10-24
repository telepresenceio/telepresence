package remotefs

import (
	"context"
	"fmt"
	"net"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/dnet"
	"github.com/telepresenceio/telepresence/v2/pkg/ipproto"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

type bridgeMounter struct {
	localPort     uint16
	sessionID     string
	managerClient manager.ManagerClient
}

func NewBridgeMounter(sessionID string, managerClient manager.ManagerClient, localPort uint16) Mounter {
	return &bridgeMounter{
		localPort:     localPort,
		sessionID:     sessionID,
		managerClient: managerClient,
	}
}

func (m *bridgeMounter) Start(ctx context.Context, _, _, _ string, podIP net.IP, port uint16) error {
	ctx = dgroup.WithGoroutineName(ctx, iputil.JoinIpPort(podIP, port))
	lc := &net.ListenConfig{}
	la := fmt.Sprintf(":%d", m.localPort)
	l, err := lc.Listen(ctx, "tcp", la)
	if err != nil {
		return err
	}
	dlog.Debugf(ctx, "Remote mount bridge listening at %s, will forward to %s", la, iputil.JoinIpPort(podIP, port))
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				dlog.Errorf(ctx, "mount listener failed: %v", err)
				return
			}
			if ctx.Err() != nil {
				return
			}
			go func() {
				if err := m.dispatchToTunnel(ctx, conn, podIP, port); err != nil {
					dlog.Error(ctx, err)
				}
			}()
		}
	}()
	return nil
}

func (m *bridgeMounter) dispatchToTunnel(ctx context.Context, conn net.Conn, podIP net.IP, port uint16) error {
	tcpAddr, ok := conn.LocalAddr().(*net.TCPAddr)
	if !ok {
		return fmt.Errorf("address %s is not a TCP address", conn.LocalAddr())
	}
	dlog.Debugf(ctx, "Opening bridge between %s and %s", tcpAddr, iputil.JoinIpPort(podIP, port))
	id := tunnel.NewConnID(ipproto.TCP, tcpAddr.IP, podIP, uint16(tcpAddr.Port), port)
	ctx, cancel := context.WithCancel(ctx)

	var s tunnel.Stream
	if pf := dnet.GetPortForwardDialer(ctx); pf != nil {
		s = tunnel.TryPortForward(ctx, id, pf, m.managerClient, m.sessionID)
	}
	if s == nil {
		ms, err := m.managerClient.Tunnel(ctx)
		if err != nil {
			cancel()
			return fmt.Errorf("failed to establish tunnel: %v", err)
		}

		tos := client.GetConfig(ctx).Timeouts()
		s, err = tunnel.NewClientStream(ctx, ms, id, m.sessionID, tos.PrivateRoundtripLatency, tos.PrivateEndpointDial)
		if err != nil {
			cancel()
			return fmt.Errorf("failed to create stream: %v", err)
		}
	}
	d := tunnel.NewConnEndpoint(s, conn, cancel, nil, nil)
	d.Start(ctx)
	<-d.Done()
	return nil
}
