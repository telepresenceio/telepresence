package driver

import (
	"context"
	"fmt"

	"github.com/docker/go-plugins-helpers/network"
	"github.com/puzpuzpuz/xsync/v4"

	"github.com/telepresenceio/clog"
)

type driver struct {
	ctx      context.Context
	pid      int
	networks *xsync.Map[string, *networkState]
}

func New(ctx context.Context, pid int) network.Driver {
	return &driver{
		ctx:      ctx,
		pid:      pid,
		networks: xsync.NewMap[string, *networkState](),
	}
}

type errNetworkNotFound string

func (e errNetworkNotFound) Error() string {
	return fmt.Sprintf("network %q not found", string(e))
}

func (d *driver) GetCapabilities() (*network.CapabilitiesResponse, error) {
	clog.Debug(d.ctx, "GetCapabilities")
	return &network.CapabilitiesResponse{
		Scope: network.LocalScope,
	}, nil
}

func (d *driver) CreateNetwork(r *network.CreateNetworkRequest) error {
	n, err := newNetwork(d.ctx, d.pid, r)
	if err != nil {
		return err
	}
	d.networks.Store(r.NetworkID, n)
	return nil
}

func (d *driver) DeleteNetwork(r *network.DeleteNetworkRequest) error {
	if n, ok := d.networks.LoadAndDelete(r.NetworkID); ok {
		clog.Debug(n.ctx, "Deleting network")
		n.cancel()
	}
	return nil
}

func (d *driver) CreateEndpoint(r *network.CreateEndpointRequest) (*network.CreateEndpointResponse, error) {
	n, ok := d.networks.Load(r.NetworkID)
	if !ok {
		return nil, errNetworkNotFound(r.NetworkID)
	}
	return n.createEndpoint(r)
}

func (d *driver) DeleteEndpoint(r *network.DeleteEndpointRequest) error {
	return d.withNetwork(r.NetworkID, func(ns *networkState) error { return ns.deleteEndpoint(r.EndpointID) })
}

func (d *driver) Join(r *network.JoinRequest) (*network.JoinResponse, error) {
	n, ok := d.networks.Load(r.NetworkID)
	if !ok {
		return nil, errNetworkNotFound(r.NetworkID)
	}
	return n.join(r)
}

func (d *driver) Leave(r *network.LeaveRequest) (err error) {
	return d.withNetwork(r.NetworkID, func(ns *networkState) error { return ns.leaveEndpoint(r.EndpointID) })
}

func (d *driver) EndpointInfo(r *network.InfoRequest) (*network.InfoResponse, error) {
	return nil, nil
}

func (d *driver) DiscoverNew(*network.DiscoveryNotification) error {
	return nil
}

func (d *driver) DiscoverDelete(*network.DiscoveryNotification) error {
	return nil
}

func (d *driver) ProgramExternalConnectivity(*network.ProgramExternalConnectivityRequest) error {
	return nil
}

func (d *driver) RevokeExternalConnectivity(*network.RevokeExternalConnectivityRequest) error {
	return nil
}

func (d *driver) AllocateNetwork(*network.AllocateNetworkRequest) (*network.AllocateNetworkResponse, error) {
	return nil, nil
}

func (d *driver) FreeNetwork(*network.FreeNetworkRequest) error {
	return nil
}

func (d *driver) withNetwork(id string, f func(*networkState) error) (err error) {
	n, ok := d.networks.Load(id)
	if !ok {
		return errNetworkNotFound(id)
	}
	return f(n)
}
