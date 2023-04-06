package vif

import (
	"context"

	"gvisor.dev/gvisor/pkg/tcpip/stack"

	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/vif/device"
	"github.com/telepresenceio/telepresence/v2/pkg/vif/netstack"
)

type TunnelVIF struct {
	stack  *stack.Stack
	Device device.Device
}

func NewTunnelVIF(ctx context.Context, tunnelStreamCreator tunnel.StreamCreator) (*TunnelVIF, error) {
	dev, err := device.OpenTun(ctx)
	if err != nil {
		return nil, err
	}
	stack, err := netstack.NewStack(ctx, dev, tunnelStreamCreator)
	if err != nil {
		return nil, err
	}
	return &TunnelVIF{
		stack:  stack,
		Device: dev,
	}, nil
}

func (vif *TunnelVIF) Close() error {
	vif.stack.Close()
	return vif.Device.Close()
}

func (vif *TunnelVIF) Run(ctx context.Context) error {
	vif.stack.Wait()
	return nil
}
