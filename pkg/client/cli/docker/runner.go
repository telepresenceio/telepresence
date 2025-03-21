package docker

import (
	"context"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/docker/docker/errdefs"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/env"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/flags"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/mount"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/spinner"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

type Runner struct {
	Flags
	ContainerName string
	Environment   map[string]string
	Mount         *mount.Info
	localMountDir string
}

func (s *Runner) Run(ctx context.Context, waitMessage string, args ...string) error {
	ud := daemon.GetUserClient(ctx)
	var runFlags *RunFlags
	if s.Flags.imageIndex > 0 {
		// arguments between the "--" separator and the image name are docker run flags, and
		// we must extract the relevant network flags.
		runArgs := args[:s.imageIndex]
		args = args[s.imageIndex:]
		var err error
		runFlags, runArgs, err = ParseRunFlags(runArgs)
		if err != nil {
			return err
		}
		s.Flags.imageIndex = len(runArgs)
		if len(runArgs) > 0 {
			args = append(runArgs, args...)
		}
		if pps := runFlags.PublishedPorts; len(pps) > 0 {
			s.Flags.PublishedPorts = append(s.Flags.PublishedPorts, pps...)
		}
		if nts := runFlags.Networks; len(nts) > 0 {
			connectCancel, err := ConnectNetworksToDaemon(ctx, runFlags.Networks, ud.DaemonID().ContainerName())
			defer connectCancel()
			if err != nil {
				return err
			}
		}
	}

	file, err := os.CreateTemp("", "tel-*.env")
	if err != nil {
		return fmt.Errorf("failed to create temporary environment file. %w", err)
	}
	defer func() {
		if err := os.Remove(file.Name()); err != nil {
			dlog.Errorf(ctx, "failed to remove temporary environment file %q: %v", file.Name(), err)
		}
		if s.localMountDir != "" {
			if err := os.RemoveAll(s.localMountDir); err != nil {
				dlog.Errorf(ctx, "failed to remove local mount directory %q: %v", s.localMountDir, err)
			}
		}
	}()

	if err = env.SyntaxDocker.WriteToFileAndClose(file, s.Environment); err != nil {
		return err
	}
	envFile := file.Name()

	// Ensure that the intercept handler is stopped properly if the daemon quits
	procCtx, cancel := context.WithCancel(ctx)
	go func() {
		if err := daemon.CancelWhenRmFromCache(procCtx, cancel, ud.DaemonID().InfoFileName()); err != nil {
			dlog.Error(ctx)
		}
	}()

	errRdr, errWrt := io.Pipe()
	procCtx = dos.WithStderr(procCtx, errWrt)
	outRdr, outWrt := io.Pipe()
	procCtx = dos.WithStdout(procCtx, outWrt)

	spin := spinner.New(ctx, "container "+s.ContainerName)
	w := s.start(procCtx, s.ContainerName, envFile, runFlags, args)
	if w.err == nil {
		w.err = ud.AddHandler(ctx, s.Environment["TELEPRESENCE_INTERCEPT_ID"], w.cmd, w.name)
		spin.Message("started")
		spin.DoneMsg(waitMessage)
		if waitMessage != "" && spin.IsNoOp() {
			ioutil.Println(dos.Stdout(ctx), waitMessage)
		}
	} else {
		_ = spin.Error(w.err)
	}
	go func() {
		_, _ = io.Copy(dos.Stdout(ctx), outRdr)
	}()
	go func() {
		_, _ = io.Copy(dos.Stderr(ctx), errRdr)
	}()

	if err = w.wait(procCtx); err != nil {
		return spin.Error(err)
	}
	spin.Done()
	return nil
}

func (s *Runner) adjustMounts(ctx context.Context, runFlags *RunFlags, args []string) ([]string, types.MountPolicies, error) {
	var mounts types.MountPolicies
	if m := s.Mount; m != nil {
		mounts = maps.Clone(m.Mounts)
		if runFlags != nil {
			if len(runFlags.Volumes) > 0 || len(runFlags.Mounts) > 0 {
				mounts = maps.Clone(mounts)
				for _, v := range runFlags.Volumes {
					dlog.Infof(ctx, "Skipping auto-mounting of path %s due to user provided -v %s", v.Target, v)
					delete(mounts, v.Target)
				}
				for _, v := range runFlags.Mounts {
					dlog.Infof(ctx, "Skipping auto-mounting of path %s due to user provided --mount %s", v.Target, v)
					delete(mounts, v.Target)
				}
			}
		}
		for path, mp := range mounts {
			if mp == types.MountPolicyLocal {
				if s.localMountDir == "" {
					var err error
					s.localMountDir, err = os.MkdirTemp("", "telfs-local-*")
					if err != nil {
						return nil, nil, err
					}
				}
				hostPath := filepath.Join(s.localMountDir, path)
				if err := os.MkdirAll(hostPath, 0o755); err != nil {
					dlog.Error(ctx, err)
					continue
				}
				ma := fmt.Sprintf("type=bind,src=%s,dst=%s", hostPath, path)
				dlog.Infof(ctx, "Adding --mount %s for remote path %s, because it has a local mount policy and is not provided by user", ma, path)
				args = append(args, "--mount", ma)
			}
		}
	}
	return args, mounts, nil
}

func (s *Runner) start(ctx context.Context, name, envFile string, runFlags *RunFlags, args []string) *waiter {
	ourArgs := []string{
		"run",
		"--env-file", envFile,
	}
	w := &waiter{name: name}
	w.mount = s.Mount

	if s.Debug {
		ourArgs = append(ourArgs, "--security-opt", "apparmor=unconfined", "--cap-add", "SYS_PTRACE")
	}

	// "--rm" is mandatory when using --docker-run, because without it, the name cannot be reused and
	// the volumes cannot be removed.
	_, set, err := flags.GetUnparsedBoolean(args, "rm")
	if err != nil {
		w.err = err
		return w
	}
	if !set {
		ourArgs = append(ourArgs, "--rm")
	}
	ourArgs, mounts, err := s.adjustMounts(ctx, runFlags, ourArgs)
	if err != nil {
		w.err = err
		return w
	}

	ud := daemon.GetUserClient(ctx)
	if !ud.Containerized() {
		// The process is containerized but the user daemon runs on the host
		for path, policy := range mounts {
			ro := ""
			switch policy {
			case types.MountPolicyIgnore, types.MountPolicyLocal:
			case types.MountPolicyRemoteReadOnly:
				ro = ",ro"
				fallthrough
			case types.MountPolicyRemote:
				ourArgs = append(ourArgs, "--mount", fmt.Sprintf("type=bind,src=%s,dst=%s%s", filepath.Join(s.Mount.LocalDir, path), path, ro))
			}
		}
		ourArgs = append(ourArgs, "--dns-search", "tel2-search")
		for _, p := range s.Flags.PublishedPorts {
			ourArgs = append(ourArgs, "-p", p.String())
		}
	} else {
		daemonName := ud.DaemonID().ContainerName()
		ourArgs = append(ourArgs, "--network", "container:"+daemonName)
		maps.DeleteFunc(mounts, func(s string, policy types.MountPolicy) bool {
			return policy == types.MountPolicyIgnore || policy == types.MountPolicyLocal
		})
		if len(mounts) > 0 {
			container := s.Environment["TELEPRESENCE_CONTAINER"]
			m := s.Mount
			w.volumes, w.err = docker.CreateVolumes(ctx, daemonName, m.Port, container, mounts, m.ReadOnly)
			if w.err != nil {
				dlog.Error(ctx, w.err)
				return w
			}
			for vol, path := range w.volumes {
				ro := ""
				if m.ReadOnly || mounts.Get("", path) == types.MountPolicyRemoteReadOnly {
					ro = ":ro"
				}
				ourArgs = append(ourArgs, "-v", fmt.Sprintf("%s:%s%s", vol, path, ro))
			}
		}
	}

	args = append(ourArgs, args...)
	w.cmd, w.err = proc.Start(context.WithoutCancel(ctx), nil, "docker", args...)
	if w.err != nil {
		return w
	}

	if ud.Containerized() {
		// Using a -p <publicPort>:<privatePort> directly on the started container was not possible because it
		// inherits the containerized daemons network config. That config includes the "telepresence" network though,
		// so we can now create socat listeners that dispatch from this network to the daemon containers network.
		daemonID := ud.DaemonID().ContainerName()
		for _, p := range s.Flags.PublishedPorts {
			var portCancel context.CancelFunc
			portCancel, w.err = startPortPublisher(ctx, daemonID, p)
			w.procsToCancel = append(w.procsToCancel, portCancel)
			if w.err != nil {
				return w
			}
		}
	}
	return w
}

type waiter struct {
	cmd *dexec.Cmd

	// err is the error (if any) produced by the run
	err error

	// name of container to stop when the run ends
	name string

	mount *mount.Info

	// volume mounts as name -> path.
	volumes map[string]string

	procsToCancel []context.CancelFunc
}

func startPortPublisher(ctx context.Context, daemonID string, p PublishedPort) (context.CancelFunc, error) {
	portCtx, portCancel := context.WithCancel(ctx)
	cidFileName, err := ioutil.CreateTempName("", "docker-run*.cid")
	if err != nil {
		return portCancel, err
	}
	_, err = proc.Start(portCtx, nil, "docker",
		"run", "--cidfile", cidFileName, "--rm", "--network", "telepresence", "-p", p.String(), "alpine/socat",
		fmt.Sprintf("%s-listen:%d,fork,reuseaddr", p.Protocol, p.ContainerPort),
		fmt.Sprintf("%s-connect:%s:%d", p.Protocol, daemonID, p.ContainerPort))
	if err != nil {
		return portCancel, err
	}
	cid, err := ReadContainerID(ctx, cidFileName)
	if err != nil {
		return portCancel, err
	}
	return func() {
		if cli, err := docker.GetClient(ctx); err == nil {
			_ = cli.ContainerKill(context.WithoutCancel(ctx), cid, "")
		}
		portCancel()
	}, nil
}

func (w *waiter) wait(ctx context.Context) error {
	if len(w.procsToCancel) > 0 {
		defer func() {
			for _, cancel := range w.procsToCancel {
				cancel()
			}
		}()
	}

	if w.err != nil {
		dlog.Error(ctx, w.err)
		return errcat.NoDaemonLogs.New(w.err)
	}

	killTimer := time.AfterFunc(math.MaxInt64, func() {
		_ = w.cmd.Process.Kill()
	})
	defer killTimer.Stop()

	var exited, signalled atomic.Bool
	volNames := make([]string, len(w.volumes))
	i := 0
	for vol := range w.volumes {
		volNames[i] = vol
		i++
	}
	afterExitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go EnsureStopContainer(afterExitCtx, w.name, volNames, &exited, &signalled, done)

	err := w.cmd.Wait()
	exited.Store(true)
	cancel()
	waitErr := <-done
	if signalled.Load() {
		// Errors caused by context or signal termination don't count.
		err = nil
	}
	if err == nil {
		err = waitErr
	}
	return errcat.NoDaemonLogs.New(err)
}

func EnsureStopContainer(ctx context.Context, containerID string, volumes []string, exited, signalled *atomic.Bool, done chan<- error) {
	defer close(done)
	if len(volumes) > 0 {
		defer func() {
			time.Sleep(200 * time.Millisecond)
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			docker.RemoveVolumes(ctx, volumes)
		}()
	}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, proc.SignalsToForward...)
	defer func() {
		signal.Stop(sigCh)
	}()
	select {
	case <-ctx.Done():
	case <-sigCh:
		signalled.Store(true)
	}
	if exited.Load() {
		dlog.Debugf(ctx, "No need to stop container %s. It already exited", containerID)
		return
	}
	ctx = context.WithoutCancel(ctx)
	ctx = docker.EnableClient(ctx)
	err := docker.StopContainer(ctx, containerID)
	if err != nil && errdefs.IsNotFound(err) {
		err = nil
	}
	done <- err
}

func ReadContainerID(ctx context.Context, cidFile string) (containerID string, err error) {
	err = backoff.Retry(func() error {
		cid, err := os.ReadFile(cidFile)
		if err != nil {
			return err
		}
		if len(cid) == 0 {
			return exec.ErrNotFound
		}
		containerID = string(cid)
		return nil
	}, backoff.WithContext(backoff.NewConstantBackOff(10*time.Millisecond), ctx))
	return containerID, err
}
