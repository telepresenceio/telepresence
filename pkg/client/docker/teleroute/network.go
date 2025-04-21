package teleroute

import (
	"context"
	"strconv"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	dockerClient "github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
)

const daemonLabel = "telepresence.io/teleroute/daemon"

func CreateNetwork(ctx context.Context, info *daemon.Info, cli *dockerClient.Client, teleroutePlugin string, teleroutePort uint16) error {
	cn := info.Name
	yes := true
	dlog.Debugf(ctx, "Creating teleroute network %s", cn)
	rsp, err := cli.NetworkCreate(ctx, cn, network.CreateOptions{
		Driver:     teleroutePlugin,
		Scope:      "local",
		Internal:   true,
		Attachable: true,
		EnableIPv4: &yes,
		Options: map[string]string{
			"host": info.ContainerIP.String(),
			"port": strconv.Itoa(int(teleroutePort)),
			"pid":  strconv.Itoa(info.ContainerPID),
		},
		Labels: map[string]string{
			daemonLabel: info.ContainerID,
		},
	})
	if err != nil {
		return err
	}
	if rsp.Warning != "" {
		dlog.Warning(ctx, rsp.Warning)
	} else {
		dlog.Debugf(ctx, "Network %s created", cn)

		// The daemon must be the first container to join the network. This join is special in that
		// it will not receive the routes that the daemon makes available. The connect is necessary
		// to open up for the daemon to communicate with other connected containers.
		err = cli.NetworkConnect(ctx, rsp.ID, info.ContainerID, &network.EndpointSettings{})
	}
	return err
}

// IsTelerouteNetwork returns true if the network was created by the teleroute driver.
func IsTelerouteNetwork(ctx context.Context, cli *dockerClient.Client, name string) bool {
	ni, err := cli.NetworkInspect(ctx, name, network.InspectOptions{})
	if err != nil {
		return false
	}
	_, ok := ni.Labels[daemonLabel]
	return ok
}

// NetworkGC deletes zombie teleroute networks. A teleroute network is considered a zombie when:
//
//  1. It is not connected to a Telepresence daemon
//  2. No container is currently connected to it.
func NetworkGC(ctx context.Context, cli *dockerClient.Client) error {
	ns, err := cli.NetworkList(ctx, network.ListOptions{Filters: filters.NewArgs(filters.KeyValuePair{
		Key:   "label",
		Value: daemonLabel,
	})})
	if err != nil {
		return err
	}

	for _, n := range ns {
		_, err := cli.ContainerInspect(ctx, n.Labels[daemonLabel])
		if errdefs.IsNotFound(err) {
			dlog.Debugf(ctx, "Garbage collecting network %s", n.Name)
			err = cli.NetworkRemove(ctx, n.ID)
			if err != nil {
				dlog.Errorf(ctx, "docker network remove %s: %v", n.ID, err)
			}
		}
	}
	return nil
}

// RemoveNetwork disconnects all containers that are connected to the network then removes the network.
func RemoveNetwork(ctx context.Context, cli *dockerClient.Client, name string) (disconnected []string, err error) {
	ni, err := cli.NetworkInspect(ctx, name, network.InspectOptions{})
	if err != nil {
		return nil, err
	}

	if nc := len(ni.Containers); nc > 0 {
		dlog.Debugf(ctx, "Disconnecting %d containers from network %s", nc, name)
		disconnected = make([]string, 0, nc)
		for cid := range ni.Containers {
			err = cli.NetworkDisconnect(ctx, name, cid, true)
			if err != nil {
				dlog.Error(ctx, err)
			} else {
				disconnected = append(disconnected, cid)
			}
		}
	}

	dlog.Debugf(ctx, "Removing network %s", name)
	return disconnected, cli.NetworkRemove(ctx, name)
}

func ReconnectNetwork(ctx context.Context, cli *dockerClient.Client, name string, disconnected []string) {
	if nc := len(disconnected); nc > 0 {
		dlog.Debugf(ctx, "Reconnecting %d containers to network %s", nc, name)
		for _, cid := range disconnected {
			err := cli.NetworkConnect(ctx, name, cid, &network.EndpointSettings{})
			if err != nil {
				dlog.Error(ctx, err)
			}
		}
	}
}
