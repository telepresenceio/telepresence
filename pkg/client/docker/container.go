package docker

import (
	"bytes"
	"context"
	"fmt"
	"net/netip"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

const Exe = "docker"

func Start(ctx context.Context, daemonInContainer bool, args ...string) (cni *ContainerInfo, cc *exec.Cmd, err error) {
	dockerOpts := []string{"create"}
	var nwName string
	if daemonInContainer {
		var dns netip.Addr
		dns, nwName, err = GetDaemonContainerNetworkInfo(ctx)
		if err != nil {
			return nil, nil, err
		}
		dockerOpts = append(dockerOpts, "--dns", dns.String())
	}

	cc = proc.StdCommand(ctx, Exe, slices.Insert(args, 0, dockerOpts...)...)
	idReader := &bytes.Buffer{}
	cc.Stdout = idReader
	cc.Env = dos.Environ(ctx)
	err = cc.Run()
	if err != nil {
		return nil, nil, err
	}
	containerID := strings.TrimSpace(idReader.String())

	ctx = EnableClient(ctx)
	cli, err := GetClient(ctx)
	if err != nil {
		return nil, nil, err
	}

	if nwName != "" {
		if err = cli.NetworkConnect(ctx, nwName, containerID, nil); err != nil {
			if !strings.Contains(err.Error(), "already exists") {
				dlog.Debugf(ctx, "failed to connect network %s to container %s: %v", nwName, containerID, err)
			}
		}
	}

	cc = proc.StdCommand(ctx, Exe, "start", "--attach", containerID)
	cc.Stdin = dos.Stdin(ctx)
	cc.Env = dos.Environ(ctx)
	err = cc.Start()
	if err != nil {
		return nil, nil, err
	}

	cni, err = GetContainerInfo(ctx, containerID, nwName)
	if err != nil {
		if errdefs.IsNotFound(err) {
			// Container is already done, so not much left to do here.
			err = cc.Wait()
		}
		return nil, nil, errcat.NoDaemonLogs.New(err)
	}
	return cni, cc, nil
}

func StopContainer(ctx context.Context, nameOrID string) error {
	cli, err := GetClient(ctx)
	if err != nil {
		return err
	}
	opts := container.StopOptions{}
	timeout := client.GetConfig(ctx).Timeouts().Get(client.TimeoutContainerShutdown)
	if timeout > 0 {
		secs := int(timeout / time.Second)
		opts.Timeout = &secs
		dlog.Debugf(ctx, "Stopping container %s with a grace period of %d seconds", nameOrID, secs)
	} else {
		dlog.Debugf(ctx, "Stopping container %s with default grace period", nameOrID)
	}
	_, err = cli.ContainerInspect(ctx, nameOrID)
	if err != nil {
		if errdefs.IsNotFound(err) {
			err = nil
		} else {
			dlog.Errorf(ctx, "Failed to inspect container %s: %v", nameOrID, err)
		}
		return err
	}
	err = cli.ContainerStop(ctx, nameOrID, opts)
	if err != nil {
		err = fmt.Errorf("failed to stop container %s: %v", nameOrID, err)
		dlog.Error(ctx, err)
		return err
	}
	dlog.Debugf(ctx, "Container %s stopped", nameOrID)
	return nil
}
