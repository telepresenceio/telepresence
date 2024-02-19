package vif

import (
	"context"

	"github.com/hashicorp/go-multierror"
	"gvisor.dev/gvisor/pkg/tcpip/stack"

	"github.com/telepresenceio/telepresence/v2/pkg/routing"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

type TunnelingDevice struct {
	stack  *stack.Stack
	Device Device
	Router *Router
	table  routing.Table
}

func NewTunnelingDevice(ctx context.Context, tunnelStreamCreator tunnel.StreamCreator) (*TunnelingDevice, error) {
	routingTable, err := routing.OpenTable(ctx)
	if err != nil {
		return nil, err
	}
	dev, err := OpenTun(ctx)
	if err != nil {
		return nil, err
	}
	stack, err := NewStack(ctx, dev, tunnelStreamCreator)
	if err != nil {
		return nil, err
	}
	router := NewRouter(dev, routingTable)
	return &TunnelingDevice{
		stack:  stack,
		Device: dev,
		Router: router,
		table:  routingTable,
	}, nil
}

func (vif *TunnelingDevice) Close(ctx context.Context) error {
	var result error
	vif.stack.Close()
	vif.Router.Close(ctx)
	if err := vif.Device.Close(); err != nil {
		result = multierror.Append(result, err)
	}
	if err := vif.table.Close(ctx); err != nil {
		result = multierror.Append(result, err)
	}
	return result
}

func (vif *TunnelingDevice) Run(ctx context.Context) error {
	vif.stack.Wait()
	vif.Device.Wait()
	return nil
}
