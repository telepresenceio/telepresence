package driver

import (
	"fmt"

	"github.com/docker/go-plugins-helpers/network"
	"github.com/puzpuzpuz/xsync/v4"
	log "github.com/sirupsen/logrus"
)

type driver struct {
	networks *xsync.Map[string, *networkState]
}

func New() network.Driver {
	return &driver{
		networks: xsync.NewMap[string, *networkState](),
	}
}

type errNetworkNotFound string

func (e errNetworkNotFound) Error() string {
	return fmt.Sprintf("network %q not found", string(e))
}

func (d *driver) GetCapabilities() (*network.CapabilitiesResponse, error) {
	log.Debug("GetCapabilities")
	return &network.CapabilitiesResponse{
		Scope: network.LocalScope,
	}, nil
}

func (d *driver) CreateNetwork(r *network.CreateNetworkRequest) (err error) {
	log.Debugf("CreateNetwork %.12s, %v", r.NetworkID, r.Options)
	defer func() {
		if err != nil {
			log.Error(err)
		}
	}()

	driverOpts, ok := r.Options["com.docker.network.generic"].(map[string]any)
	if !ok {
		return fmt.Errorf("network options are missing com.docker.network.generic")
	}
	ns := newNetworkState()
	err = ns.options.parse(driverOpts)
	if err != nil {
		return err
	}
	err = ns.initialize()
	if err != nil {
		return err
	}
	d.networks.Store(r.NetworkID, ns)
	go func() {
		err = d.watchRoutes(r.NetworkID)
		if err != nil {
			log.Error(err)
		}
	}()
	return nil
}

func (d *driver) DeleteNetwork(r *network.DeleteNetworkRequest) error {
	log.Debugf("DeleteNetwork %.12s", r.NetworkID)
	return d.deleteNetworkResources(r.NetworkID)
}

func (d *driver) CreateEndpoint(r *network.CreateEndpointRequest) (*network.CreateEndpointResponse, error) {
	log.Debugf("CreateEndpoint %.12s %.12s, %v", r.NetworkID, r.EndpointID, r.Options)
	return &network.CreateEndpointResponse{}, d.withNetwork(r.NetworkID, func(ns *networkState) error { return ns.createEndpoint(r.EndpointID) })
}

func (d *driver) DeleteEndpoint(r *network.DeleteEndpointRequest) error {
	log.Debugf("DeleteEndpoint %.12s %.12s", r.NetworkID, r.EndpointID)
	return d.withNetwork(r.NetworkID, func(ns *networkState) error { return ns.deleteEndpoint(r.EndpointID) })
}

func (d *driver) Join(r *network.JoinRequest) (*network.JoinResponse, error) {
	log.Debugf("Join %.12s %.12s, %v", r.NetworkID, r.EndpointID, r.Options)
	rsp := &network.JoinResponse{DisableGatewayService: true}
	err := d.withNetwork(r.NetworkID, func(n *networkState) (err error) {
		rsp.InterfaceName, rsp.StaticRoutes, err = n.join(r.EndpointID)
		return err
	})
	if err != nil {
		log.Error(err)
		return nil, err
	}
	return rsp, nil
}

func (d *driver) Leave(r *network.LeaveRequest) (err error) {
	log.Debugf("Leave %.12s %.12s", r.NetworkID, r.EndpointID)
	return nil
}

func (d *driver) EndpointInfo(r *network.InfoRequest) (*network.InfoResponse, error) {
	log.Debugf("EndpointInfo %.12s %.12s", r.NetworkID, r.EndpointID)
	return &network.InfoResponse{Value: map[string]string{}}, nil
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

// deleteNetworkResources will delete the network from the driver. This means that any containers
// to the network will lose the routes that the network provides. The network will still
// be present in the docker engine though (the plugin has no way to propagate this delete), but
// new connections to it will be rejected.
func (d *driver) deleteNetworkResources(networkID string) error {
	log.Debugf("deleteNetworkResources %.12s", networkID)
	n, ok := d.networks.LoadAndDelete(networkID)
	if !ok {
		return errNetworkNotFound(networkID)
	}
	return n.deleteResources()
}

func (d *driver) watchRoutes(networkID string) error {
	n, ok := d.networks.Load(networkID)
	if !ok {
		return errNetworkNotFound(networkID)
	}

	defer func() {
		// Deleting the network would require access to the docker daemon, which we don't have. So
		// we just free up the resources here by deleting our bridge and all veths.
		err := d.deleteNetworkResources(networkID)
		if err != nil {
			log.Errorf("error deleting network %s: %v", networkID, err)
		}
	}()
	return n.watchRoutes()
}

func (d *driver) withNetwork(id string, f func(*networkState) error) (err error) {
	n, ok := d.networks.Load(id)
	if !ok {
		return errNetworkNotFound(id)
	}
	return f(n)
}
