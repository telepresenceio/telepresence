package vif

import (
	"context"

	"gvisor.dev/gvisor/pkg/tcpip/stack"

	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

type TunnelingDevice struct {
	stack  *stack.Stack
	Device Device
}

func NewTunnelingDevice(ctx context.Context, tunnelStreamCreator tunnel.StreamCreator) (*TunnelingDevice, error) {
	dev, err := OpenTun(ctx)
	if err != nil {
		return nil, err
	}
	stack, err := NewStack(ctx, dev, tunnelStreamCreator)
	if err != nil {
		return nil, err
	}
	return &TunnelingDevice{
		stack:  stack,
		Device: dev,
	}, nil
}

func (vif *TunnelingDevice) Close() error {
	vif.stack.Close()
	return vif.Device.Close()
}

func (vif *TunnelingDevice) Run(ctx context.Context) error {
	vif.stack.Wait()
	return nil
}
