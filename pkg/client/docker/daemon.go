// Package docker contains the functions necessary to start or discover a Telepresence daemon running in a docker container.
package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/dnet"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

const (
	telepresenceImage = "telepresence" // TODO: Point to docker.io/datawire and make it configurable
	dockerTpCache     = "/root/.cache/telepresence"
	dockerTpConfig    = "/root/.config/telepresence"
	dockerTpLog       = "/root/.cache/telepresence/logs"
)

// ClientImage returns the fully qualified name of the docker image that corresponds to
// the version of the current executable.
func ClientImage(ctx context.Context) string {
	registry := client.GetConfig(ctx).Images.Registry(ctx)
	return registry + "/" + telepresenceImage + ":" + strings.TrimPrefix(version.Version, "v")
}

// EnsureNetwork checks if that a network with the given name exists, and creates it if that is not the case.
func EnsureNetwork(ctx context.Context, name string) {
	// Ensure that the telepresence bridge network exists
	cmd := dexec.CommandContext(ctx, "docker", "network", "create", name)
	cmd.DisableLogging = true
	_ = cmd.Run()
}

// DaemonOptions returns the options necessary to pass to a docker run when starting a daemon container.
func DaemonOptions(ctx context.Context, name string, kubeConfig string) ([]string, *net.TCPAddr, error) {
	tpConfig, err := filelocation.AppUserConfigDir(ctx)
	if err != nil {
		return nil, nil, errcat.NoDaemonLogs.New(err)
	}
	tpCache, err := filelocation.AppUserCacheDir(ctx)
	if err != nil {
		return nil, nil, errcat.NoDaemonLogs.New(err)
	}
	tpLog, err := filelocation.AppUserLogDir(ctx)
	if err != nil {
		return nil, nil, errcat.NoDaemonLogs.New(err)
	}
	if kubeConfig == "" {
		kubeConfig = clientcmd.RecommendedHomeFile
	}
	as, err := dnet.FreePortsTCP(1)
	if err != nil {
		return nil, nil, err
	}
	addr := as[0]
	port := addr.Port
	opts := []string{
		"--name", name,
		"--network", "telepresence",
		"--cap-add", "NET_ADMIN",
		"--device", "/dev/net/tun:/dev/net/tun",
		"-e", fmt.Sprintf("TELEPRESENCE_UID=%d", os.Getuid()),
		"-e", fmt.Sprintf("TELEPRESENCE_GID=%d", os.Getgid()),
		"-p", fmt.Sprintf("%s:%d", addr, port),
		"-v", fmt.Sprintf("%s:/root/.kube/config", kubeConfig),
		"-v", fmt.Sprintf("%s:%s:ro", tpConfig, dockerTpConfig),
		"-v", fmt.Sprintf("%s:%s", tpCache, dockerTpCache),
		"-v", fmt.Sprintf("%s:%s", tpLog, dockerTpLog),
	}
	env := client.GetEnv(ctx)
	if env.ScoutDisable {
		opts = append(opts, "-e", "SCOUT_DISABLE=1")
	}
	return opts, addr, nil
}

// DaemonArgs returns the arguments to pass to a docker run when starting a container daemon.
func DaemonArgs(name string, port int) []string {
	return []string{
		"connector-foreground",
		"--name", "docker-" + name,
		"--address", fmt.Sprintf(":%d", port),
		"--embed-network",
	}
}

// DiscoverDaemon searches the daemon cache for an entry corresponding to the given name. A connection
// to that daemon is returned if such an entry is found.
func DiscoverDaemon(ctx context.Context, name string) (conn *grpc.ClientConn, err error) {
	port, err := cache.DaemonPortForName(ctx, name)
	if err != nil {
		return nil, err
	}
	var addr string
	if RunningInContainer() {
		// Containers use the daemon container DNS name
		addr = fmt.Sprintf("%s:%d", name, port)
	} else {
		// The host relies on that the daemon has exposed a port to localhost
		addr = fmt.Sprintf(":%d", port)
	}
	return ConnectDaemon(ctx, addr)
}

// ConnectDaemon connects to a daemon at the given address.
func ConnectDaemon(ctx context.Context, address string) (conn *grpc.ClientConn, err error) {
	// Assume that the user daemon is running and connect to it using the given address instead of using a socket.
	return grpc.DialContext(ctx, address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithNoProxy(),
		grpc.WithBlock(),
		grpc.FailOnNonTempDialError(true))
}

// PullImage checks if the given image exists locally by doing docker image inspect. A docker pull is
// performed if no local image is found. Stdout is silenced during those operations.
func PullImage(ctx context.Context, image string) error {
	cmd := proc.StdCommand(ctx, "docker", "image", "inspect", image)
	cmd.Stderr = io.Discard
	cmd.Stdout = io.Discard
	if cmd.Run() == nil {
		// Image exists in the local cache, so don't bother pulling it.
		return nil
	}
	cmd = proc.StdCommand(ctx, "docker", "pull", image)
	// Docker run will put the pull logs in stderr, but docker pull will put them in stdout.
	// We discard them here, so they don't spam the user. They'll get errors through stderr if it comes to it.
	cmd.Stdout = io.Discard
	return cmd.Run()
}

// LaunchDaemon ensures that the image returned by ClientImage exists by calling PullImage. It then uses the
// options DaemonOptions and DaemonArgs to start the image, and finally ConnectDaemon to connect to it. A
// successful start yields a cache.DaemonInfo entry in the cache.
func LaunchDaemon(ctx context.Context, name, kubeconfig string) (conn *grpc.ClientConn, err error) {
	if RunningInContainer() {
		return nil, errors.New("unable to start a docker container from within a container")
	}
	cidFileName, err := ioutil.CreateTempName("", "cid*.txt")
	if err != nil {
		return nil, fmt.Errorf("failed to create cidfile: %v", err)
	}
	image := ClientImage(ctx)
	if err = PullImage(ctx, image); err != nil {
		return nil, err
	}
	EnsureNetwork(ctx, "telepresence")
	opts, addr, err := DaemonOptions(ctx, name, kubeconfig)
	if err != nil {
		return nil, errcat.NoDaemonLogs.New(err)
	}
	args := DaemonArgs(name, addr.Port)

	allArgs := make([]string, 0, len(opts)+len(args)+4)
	allArgs = append(allArgs,
		"run",
		"--rm",
		"--cidfile", cidFileName,
	)
	allArgs = append(allArgs, opts...)
	allArgs = append(allArgs, image)
	allArgs = append(allArgs, args...)

	cmd := proc.StdCommand(ctx, "docker", allArgs...)
	if err := cmd.Start(); err != nil {
		return nil, errcat.NoDaemonLogs.New(err)
	}

	cidFound := make(chan string, 1)
	errStart := make(chan error, 1)
	go func() {
		defer close(cidFound)
		for ctx.Err() == nil {
			dtime.SleepWithContext(ctx, 50*time.Millisecond)
			if _, err := os.Stat(cidFileName); err == nil {
				dtime.SleepWithContext(ctx, 200*time.Millisecond)
				cid, err := os.ReadFile(cidFileName)
				if err == nil {
					cidFound <- string(cid)
					return
				}
			}
		}
	}()
	go func() {
		defer close(errStart)
		if err := cmd.Wait(); err != nil {
			err = fmt.Errorf("daemon container exited with %v", err)
			dlog.Error(ctx, err)
			errStart <- err
		} else {
			dlog.Debug(ctx, "daemon container exited normally")
		}
	}()
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done(): // Everything is cancelled
	case cid := <-cidFound: // Success, the daemon info file exists
		err := cache.SaveDaemonInfo(ctx,
			&cache.DaemonInfo{
				Options:  map[string]string{"cid": cid},
				InDocker: true,
			}, cache.DaemonInfoFile(name, addr.Port))
		if err != nil {
			return nil, err
		}
		// Give the listener time to start
		dtime.SleepWithContext(ctx, 500*time.Millisecond)
	case err := <-errStart: // Daemon exited before the daemon info came into existence
		return nil, errcat.NoDaemonLogs.New(err)
	}
	return ConnectDaemon(ctx, addr.String())
}

// RunningInContainer returns true if the current process runs from inside a docker container.
func RunningInContainer() bool {
	_, err := os.Stat("/.dockerenv")
	return err == nil
}
