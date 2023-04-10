package vif

import (
	"context"

	"gvisor.dev/gvisor/pkg/tcpip/stack"

	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

type TunnelingDevice struct {
	stack  *stack.Stack
	Device Device
	Router *Router
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
	router := NewRouter(dev)
	return &TunnelingDevice{
		stack:  stack,
		Device: dev,
		Router: router,
	}, nil
}

func (vif *TunnelingDevice) Close(ctx context.Context) error {
	vif.stack.Close()
	vif.Router.Close(ctx)
	return vif.Device.Close()
}

func (vif *TunnelingDevice) Run(ctx context.Context) error {
	vif.stack.Wait()
	return nil
}
