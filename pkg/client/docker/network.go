package docker

import (
	"context"
	"fmt"
	"math/rand"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/network"
	dockerClient "github.com/docker/docker/client"

	"github.com/datawire/dlib/dlog"
)

// EnsureNetwork checks if a network with the given name exists, and creates it if that is not the case.
func EnsureNetwork(ctx context.Context, name string) error {
	cli, err := GetClient(ctx)
	if err != nil {
		return err
	}
	resource, err := cli.NetworkInspect(ctx, name, types.NetworkInspectOptions{})
	if err != nil {
		if !dockerClient.IsErrNotFound(err) {
			return fmt.Errorf("docker network inspect failed: %w", err)
		}
	} else {
		// this is required, or services like apache will fail to do DNS lookups (even
		// if IPv6 is not used).
		if resource.EnableIPv6 {
			dlog.Debugf(ctx, "found IPv6 enabled network %s", name)
			return nil
		}
		dlog.Infof(ctx, "network %s does not have IPv6 enabled. Will attempt to recreate it", name)
		if err = cli.NetworkRemove(ctx, name); err != nil {
			dlog.Warnf(ctx, "failed to remove network %s. A network without IPv6 can impact DNS badly, even when IPv6 is not used", name)
		}
	}

	// Make an attempt to create the network with IPv6 enabled. This will fail unless the user has enabled
	// IPv6 in /etc/docker/daemon.json.
	rsp, err := cli.NetworkCreate(ctx, name, types.NetworkCreate{
		CheckDuplicate: false,
		Driver:         "bridge",
		Scope:          "local",
		EnableIPv6:     true,
	})

	if err == nil {
		// Creation of the IPv6 enabled network succeeded, so we're done here.
		if rsp.Warning != "" {
			dlog.Warningf(ctx, "network create %s: %s", name, rsp.Warning)
		} else {
			dlog.Debugf(ctx, "network create: %s", name)
		}
		return nil
	}

	// IPv6 is probably not configured in /etc/docker/daemon.yaml.
	if !strings.Contains(err.Error(), "non-overlapping IPv6 address") {
		// Some other error prevented us from creating the network.
		return err
	}

	// Adding an IPv6 subnet and gateway in addition to enabling IPv6 should work even
	// when no IPv6 is enabled in /etc/docker/daemon.yaml

	// First, create a dummy network without IPv6 so that we get a proper IPAM config
	dummyNet, err := cli.NetworkCreate(ctx, fmt.Sprintf("tp-dummy-%08x", rand.Int31()), types.NetworkCreate{
		CheckDuplicate: false,
		Driver:         "bridge",
		Scope:          "local",
		EnableIPv6:     false,
	})
	if err != nil {
		return nil
	}
	resource, err = cli.NetworkInspect(ctx, dummyNet.ID, types.NetworkInspectOptions{})
	if dummyErr := cli.NetworkRemove(ctx, dummyNet.ID); dummyErr != nil {
		dlog.Warnf(ctx, "failed to remove network %s: %v", dummyNet.ID, dummyErr)
	}
	if err != nil {
		return err
	}
	ipam := &resource.IPAM

	// Save original Config to use a source for copy
	numConfigs := len(ipam.Config)
	ipamConfig := make([]network.IPAMConfig, numConfigs)
	copy(ipamConfig, ipam.Config)

	// Make attempts to add a random IPv6 subnet and gateway to IPAM
	for retry := 0; retry < 5; retry++ {
		ipn := fmt.Sprintf("%016x", rand.Int63())
		ipn = fmt.Sprintf("%s:%s:%s:%s::", ipn[:4], ipn[4:8], ipn[8:12], ipn[12:])
		ipam.Config = make([]network.IPAMConfig, numConfigs+1)
		copy(ipam.Config, ipamConfig)
		ipam.Config[numConfigs] = network.IPAMConfig{
			Subnet:  fmt.Sprintf("%s/64", ipn),
			Gateway: fmt.Sprintf("%s1", ipn),
		}

		// Create the IPv6 enabled network
		_, err = cli.NetworkCreate(ctx, name, types.NetworkCreate{
			CheckDuplicate: false,
			Driver:         "bridge",
			Scope:          "local",
			EnableIPv6:     true,
			IPAM:           ipam,
		})
		if err == nil {
			return nil
		}
		err = fmt.Errorf("failed to create network with random IPv6 subnet: %w", err)
		dlog.Debug(ctx, err)
	}
	return err
}
