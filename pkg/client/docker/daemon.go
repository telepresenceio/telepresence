// Package docker contains the functions necessary to start or discover a Telepresence daemon running in a docker container.
package docker

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/v2/pkg/authenticator/patcher"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker/kubeauth"
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
func DaemonOptions(ctx context.Context, name string) ([]string, *net.TCPAddr, error) {
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
		"-v", fmt.Sprintf("%s:%s:ro", tpConfig, dockerTpConfig),
		"-v", fmt.Sprintf("%s:%s", tpCache, dockerTpCache),
		"-v", fmt.Sprintf("%s:%s", tpLog, dockerTpLog),
	}
	if runtime.GOOS == "linux" {
		opts = append(opts, "--add-host", "host.docker.internal:host-gateway")
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
	if proc.RunningInContainer() {
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

const kubeAuthPortFile = kubeauth.CommandName + ".port"

func readPortFile(ctx context.Context, portFile string, configFiles []string) (uint16, error) {
	pb, err := os.ReadFile(portFile)
	if err != nil {
		return 0, err
	}
	var p kubeauth.PortFile
	err = json.Unmarshal(pb, &p)
	if err == nil {
		if p.Kubeconfig == strings.Join(configFiles, string(filepath.ListSeparator)) {
			return uint16(p.Port), nil
		}
		dlog.Debug(ctx, "kubeconfig used by kubeauth is no longer valid")
	}
	if err := os.Remove(portFile); err != nil {
		return 0, err
	}
	return 0, os.ErrNotExist
}

func appendKubeFlags(kubeFlags map[string]string, args []string) ([]string, error) {
	for k, v := range kubeFlags {
		switch k {
		case "as-group":
			// Multi valued
			r := csv.NewReader(strings.NewReader(v))
			gs, err := r.Read()
			if err != nil {
				return nil, err
			}
			for _, g := range gs {
				args = append(args, "--"+k, g)
			}
		case "disable-compression", "insecure-skip-tls-verify":
			// Boolean with false default.
			if v != "false" {
				args = append(args, "--"+k)
			}
		default:
			args = append(args, "--"+k, v)
		}
	}
	return args, nil
}

func startAuthenticatorService(ctx context.Context, portFile string, kubeFlags map[string]string, configFiles []string) (uint16, error) {
	// remove any stale port file
	_ = os.Remove(portFile)

	args := make([]string, 0, 4+len(kubeFlags)*2)
	args = append(args, client.GetExe(), kubeauth.CommandName, "--portfile", portFile)
	var err error
	if args, err = appendKubeFlags(kubeFlags, args); err != nil {
		return 0, err
	}
	if err := proc.StartInBackground(true, args...); err != nil {
		return 0, err
	}

	// Wait for the new port file to emerge
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		dtime.SleepWithContext(ctx, 10*time.Millisecond)
		port, err := readPortFile(ctx, portFile, configFiles)
		if err != nil {
			if !os.IsNotExist(err) {
				return 0, err
			}
			continue
		}
		return port, nil
	}
	return 0, fmt.Errorf(`timeout while waiting for "%s %s" to create a port file`, client.GetExe(), kubeauth.CommandName)
}

func ensureAuthenticatorService(ctx context.Context, kubeFlags map[string]string, configFiles []string) (uint16, error) {
	tpCache, err := filelocation.AppUserCacheDir(ctx)
	if err != nil {
		return 0, err
	}
	portFile := filepath.Join(tpCache, kubeAuthPortFile)
	st, err := os.Stat(portFile)
	if err != nil {
		if !os.IsNotExist(err) {
			return 0, err
		}
	} else if st.ModTime().Add(kubeauth.PortFileStaleTime).After(time.Now()) {
		port, err := readPortFile(ctx, portFile, configFiles)
		if err == nil {
			dlog.Debug(ctx, "kubeauth service found alive and valid")
			return port, nil
		}
		if !os.IsNotExist(err) {
			return 0, err
		}
	}
	return startAuthenticatorService(ctx, portFile, kubeFlags, configFiles)
}

func EnableK8SAuthenticator(ctx context.Context) error {
	cr := connect.GetRequest(ctx)
	dlog.Debugf(ctx, "kubeflags = %v", cr.KubeFlags)
	configFlags, err := client.ConfigFlags(cr.KubeFlags)
	if err != nil {
		return err
	}
	loader := configFlags.ToRawKubeConfigLoader()
	configFiles := loader.ConfigAccess().GetLoadingPrecedence()
	dlog.Debugf(ctx, "config = %v", configFiles)
	config, err := loader.RawConfig()
	if err != nil {
		return err
	}

	// Minify the config so that we only deal with the current context.
	if cx := configFlags.Context; cx != nil && *cx != "" {
		config.CurrentContext = *cx
	}
	if err = api.MinifyConfig(&config); err != nil {
		return err
	}
	dlog.Debugf(ctx, "context = %v", config.CurrentContext)

	if patcher.NeedsStubbedExec(&config) {
		port, err := ensureAuthenticatorService(ctx, cr.KubeFlags, configFiles)
		if err != nil {
			return err
		}
		// Replace any auth exec with a stub
		addr := fmt.Sprintf("host.docker.internal:%d", port)
		if err := patcher.ReplaceAuthExecWithStub(&config, addr); err != nil {
			return err
		}
	}

	// Store the file using its context name under the <telepresence cache>/kube directory
	const kubeConfigs = "kube"
	kubeConfigFile := config.CurrentContext
	tpCache, err := filelocation.AppUserCacheDir(ctx)
	if err != nil {
		return err
	}
	kubeConfigDir := filepath.Join(tpCache, kubeConfigs)
	if err = os.MkdirAll(kubeConfigDir, 0o700); err != nil {
		return err
	}
	if err = clientcmd.WriteToFile(config, filepath.Join(kubeConfigDir, kubeConfigFile)); err != nil {
		return err
	}

	// Concatenate using "/". This will be used in linux
	cr.KubeFlags["kubeconfig"] = fmt.Sprintf("%s/%s/%s", dockerTpCache, kubeConfigs, kubeConfigFile)
	return nil
}

// LaunchDaemon ensures that the image returned by ClientImage exists by calling PullImage. It then uses the
// options DaemonOptions and DaemonArgs to start the image, and finally ConnectDaemon to connect to it. A
// successful start yields a cache.DaemonInfo entry in the cache.
func LaunchDaemon(ctx context.Context, name string) (conn *grpc.ClientConn, err error) {
	if proc.RunningInContainer() {
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
	opts, addr, err := DaemonOptions(ctx, name)
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
				Options:     map[string]string{"cid": cid},
				InDocker:    true,
				DaemonPort:  addr.Port,
				KubeContext: name,
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
