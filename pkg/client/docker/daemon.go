package docker

import (
	"context"
	"fmt"
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

func ClientImage(ctx context.Context) string {
	registry := client.GetConfig(ctx).Images.Registry(ctx)
	return registry + "/" + telepresenceImage + ":" + strings.TrimPrefix(version.Version, "v")
}

func EnsureNetwork(ctx context.Context, name string) {
	// Ensure that the telepresence bridge network exists
	cmd := dexec.CommandContext(ctx, "docker", "network", "create", name)
	cmd.DisableLogging = true
	_ = cmd.Run()
}

func DaemonOptions(ctx context.Context, name string, kubeConfig string) ([]string, string, *net.TCPAddr, error) {
	tpConfig, err := filelocation.AppUserConfigDir(ctx)
	if err != nil {
		return nil, "", nil, errcat.NoDaemonLogs.New(err)
	}
	tpCache, err := filelocation.AppUserCacheDir(ctx)
	if err != nil {
		return nil, "", nil, errcat.NoDaemonLogs.New(err)
	}
	tpLog, err := filelocation.AppUserLogDir(ctx)
	if err != nil {
		return nil, "", nil, errcat.NoDaemonLogs.New(err)
	}
	cidFileName, err := ioutil.CreateTempName("", "cid*.txt")
	if err != nil {
		return nil, "", nil, fmt.Errorf("failed to create cidfile: %v", err)
	}
	if kubeConfig == "" {
		kubeConfig = clientcmd.RecommendedHomeFile
	}
	as, err := dnet.FreePortsTCP(1)
	if err != nil {
		return nil, "", nil, err
	}
	addr := as[0]
	port := addr.Port
	return []string{
		"run",
		"--rm",
		"--cidfile", cidFileName,
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
		"--quiet",
	}, cidFileName, addr, nil
}

func DaemonArgs(port int) []string {
	return []string{
		"connector-foreground",
		"--address", fmt.Sprintf(":%d", port),
		"--embed-network",
	}
}

func DiscoverDaemon(ctx context.Context, name string) (conn *grpc.ClientConn, err error) {
	port, err := cache.DaemonPortForName(ctx, name)
	if err != nil {
		return nil, err
	}
	// Assume that the user daemon is running and connect to it using the given address instead of using a socket.
	return grpc.DialContext(ctx, fmt.Sprintf(":%d", port),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithNoProxy(),
		grpc.WithBlock(),
		grpc.FailOnNonTempDialError(true))
}

func ConnectDaemon(ctx context.Context, address string) (conn *grpc.ClientConn, err error) {
	// Assume that the user daemon is running and connect to it using the given address instead of using a socket.
	return grpc.DialContext(ctx, address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithNoProxy(),
		grpc.WithBlock(),
		grpc.FailOnNonTempDialError(true))
}

func LaunchDaemon(ctx context.Context, name, kubeconfig string) (conn *grpc.ClientConn, err error) {
	EnsureNetwork(ctx, "telepresence")
	opts, cidFileName, addr, err := DaemonOptions(ctx, name, kubeconfig)
	if err != nil {
		return nil, errcat.NoDaemonLogs.New(err)
	}
	args := DaemonArgs(addr.Port)
	allArgs := make([]string, 0, len(opts)+len(args)+1)
	allArgs = append(allArgs, opts...)
	allArgs = append(allArgs, ClientImage(ctx))
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
