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
	dockerClient "github.com/moby/moby/client"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/flags"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

const Exe = "docker"

// Start creates and starts a container. If the container was created but a later step fails, the
// container is removed and the returned ContainerInfo has only its ID set.
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
	created := &ContainerInfo{ID: containerID}
	failed := true
	defer func() {
		if failed {
			removeContainer(ctx, containerID)
		}
	}()

	cli, err := GetClient(ctx)
	if err != nil {
		return created, nil, err
	}

	if nwName != "" {
		if _, err = cli.NetworkConnect(ctx, nwName, dockerClient.NetworkConnectOptions{Container: containerID}); err != nil {
			if !strings.Contains(err.Error(), "already exists") {
				clog.Debugf(ctx, "failed to connect network %s to container %s: %v", nwName, containerID, err)
			}
		}
	}

	startArgs := []string{"start", "--attach"}
	if flags.HasOption("interactive", 'i', RunOptions(args)) {
		startArgs = append(startArgs, "--interactive")
	}
	startArgs = append(startArgs, containerID)
	cc = proc.StdCommand(ctx, Exe, startArgs...)
	cc.Stdin = dos.Stdin(ctx)
	cc.Env = dos.Environ(ctx)
	err = cc.Start()
	if err != nil {
		return created, nil, err
	}

	cni, err = GetContainerInfo(ctx, containerID, nwName)
	if err != nil {
		if errdefs.IsNotFound(err) {
			// Container is already done, so not much left to do here.
			failed = false
			err = cc.Wait()
			return nil, nil, errcat.NoDaemonLogs.New(err)
		}
		return created, nil, errcat.NoDaemonLogs.New(err)
	}
	failed = false
	return cni, cc, nil
}

// removeContainer force-removes a container, ignoring errors.
func removeContainer(ctx context.Context, id string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	cli, err := GetClient(ctx)
	if err != nil {
		return
	}
	if _, err = cli.ContainerRemove(ctx, id, dockerClient.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
		clog.Debugf(ctx, "failed to remove container %s: %v", id, err)
	}
}

func StopContainer(ctx context.Context, nameOrID string) error {
	cli, err := GetClient(ctx)
	if err != nil {
		return err
	}
	opts := dockerClient.ContainerStopOptions{}
	timeout := client.GetConfig(ctx).Timeouts().Get(client.TimeoutContainerShutdown)
	if timeout > 0 {
		secs := int(timeout / time.Second)
		opts.Timeout = &secs
		clog.Debugf(ctx, "Stopping container %s with a grace period of %d seconds", nameOrID, secs)
	} else {
		clog.Debugf(ctx, "Stopping container %s with default grace period", nameOrID)
	}
	_, err = cli.ContainerInspect(ctx, nameOrID, dockerClient.ContainerInspectOptions{})
	if err != nil {
		if errdefs.IsNotFound(err) {
			err = nil
		} else {
			clog.Errorf(ctx, "Failed to inspect container %s: %v", nameOrID, err)
		}
		return err
	}
	_, err = cli.ContainerStop(ctx, nameOrID, opts)
	if err != nil {
		err = fmt.Errorf("failed to stop container %s: %v", nameOrID, err)
		clog.Error(ctx, err)
		return err
	}
	clog.Debugf(ctx, "Container %s stopped", nameOrID)
	return nil
}
