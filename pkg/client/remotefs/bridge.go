package remotefs

import (
	"context"
	"net/netip"

	"github.com/telepresenceio/dlib/v2/dgroup"
	"github.com/telepresenceio/dlib/v2/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/forwarder"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

type bridgeMounter uint16

func NewBridgeMounter(_ tunnel.SessionID, _ manager.ManagerClient, localPort uint16) Mounter {
	return bridgeMounter(localPort)
}

func (m bridgeMounter) Start(ctx context.Context, _, _, _, _ string, podAddrPort netip.AddrPort, _ bool) error {
	ctx = dgroup.WithGoroutineName(ctx, "/"+podAddrPort.String())
	pp := types.PortAndProto{
		Port:  uint16(m),
		Proto: types.ProtoTCP,
	}
	dlog.Debugf(ctx, "Remote mount bridge listening at :%d, will forward to %s", m, podAddrPort)
	go func() {
		f := forwarder.New(pp, tunnel.ClientToAgent, podAddrPort)
		err := f.Serve(ctx, nil)
		if err != nil && ctx.Err() == nil {
			dlog.Errorf(ctx, "port-forwarder failed with %v", err)
		}
	}()
	return nil
}
