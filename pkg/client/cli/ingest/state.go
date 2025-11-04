package ingest

import (
	"context"
	"fmt"
	"os"
	"runtime"

	grpcCodes "google.golang.org/grpc/codes"
	grpcStatus "google.golang.org/grpc/status"

	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	cliDocker "github.com/telepresenceio/telepresence/v2/pkg/client/cli/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/progress"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

type State interface {
	CreateRequest() (*rpc.IngestRequest, error)
	Run(context.Context) error
	RunAndLeave() bool
}

type state struct {
	*Command
	mountError       error
	info             *rpc.IngestInfo
	handlerContainer string

	// Possibly extended version of the state. Use when calling interface methods.
	self State
}

func NewState(
	args *Command,
	mountError error,
) State {
	s := &state{
		Command:    args,
		mountError: mountError,
	}
	s.self = s
	return s
}

func (s *state) SetSelf(self State) {
	s.self = self
}

func (s *state) CreateRequest() (*rpc.IngestRequest, error) {
	ir := &rpc.IngestRequest{
		Identifier: &rpc.IngestIdentifier{
			WorkloadName:  s.WorkloadName,
			ContainerName: s.ContainerName,
		},
		LocalMountPort: int32(s.MountFlags.LocalMountPort),
		MountPoint:     s.MountFlags.Mount,
	}

	for _, toPod := range s.ToPod {
		pp, err := types.ParsePortAndProto(toPod)
		if err != nil {
			return nil, err
		}
		ir.LocalPorts = append(ir.LocalPorts, pp.String())
	}
	return ir, nil
}

func (s *state) RunAndLeave() bool {
	return len(s.Cmdline) > 0 || s.DockerFlags.Run
}

func (s *state) Run(ctx context.Context) error {
	progress.Start(ctx, "Initializing")
	var err error
	if !s.RunAndLeave() {
		return client.WithEnsuredState(ctx, s.create, nil, nil)
	}

	// start intercept, run command, then leave the intercept
	if s.DockerFlags.Run {
		var defaultContainerName string
		if len(s.ContainerName) > 0 {
			defaultContainerName = fmt.Sprintf("ingest-%s-%s", s.WorkloadName, s.ContainerName)
		} else {
			defaultContainerName = fmt.Sprintf("ingest-%s", s.WorkloadName)
		}
		ctx = docker.EnableClient(ctx)
		err = s.DockerFlags.PullOrBuildImage(progress.WithEventId(ctx, "Handler"))
		if err != nil {
			return err
		}
		s.handlerContainer, s.Cmdline, err = s.DockerFlags.GetContainerNameAndArgs(defaultContainerName)
		if err != nil {
			return err
		}
	}
	return client.WithEnsuredState(ctx, s.create, s.runCommand, s.leave)
}

func (s *state) create(ctx context.Context) (acquired bool, err error) {
	ud := daemon.MustGetUserClient(ctx)
	ir, err := s.self.CreateRequest()
	if err != nil {
		return false, errcat.NoDaemonLogs.New(err)
	}

	if ir.MountPoint != "" {
		defer func() {
			if !acquired && runtime.GOOS != "windows" {
				// remove if empty
				_ = os.Remove(ir.MountPoint)
			}
		}()
	}

	progress.Start(ctx, "Creating")
	defer progress.Stop(ctx)

	// Submit the request
	ctx = progress.WithEventId(ctx, ud.DaemonID().Name)
	progress.Working(ctx, types.EngagementTypeIngest.Working())
	ii, err := ud.Ingest(ctx, ir)
	if err != nil {
		return false, progress.MaybeWriteError(ctx, grpc.FromGRPC(err))
	}

	if s.MountFlags.Enabled {
		if ir.LocalMountPort != 0 {
			ii.PodIp = "127.0.0.1"
			ii.SftpPort = ir.LocalMountPort
		}
	} else {
		ii.MountPoint = ""
		ii.FtpPort = 0
		ii.SftpPort = 0
	}
	s.info = ii

	progress.Done(ctx, types.EngagementTypeIngest.WorkDone())
	progress.Infof(ctx, "Using %s %s", ii.WorkloadKind, ii.Workload)

	env := s.info.Environment
	if env == nil {
		env = make(map[string]string)
		s.info.Environment = env
	}
	env["TELEPRESENCE_ROOT"] = s.info.ClientMountPoint
	if err = s.EnvFlags.MaybeWrite(env); err != nil {
		return true, err
	}
	s.ContainerName = env["TELEPRESENCE_CONTAINER"]
	info := NewInfo(ctx, ii, nil)
	if s.FormattedOutput {
		output.Object(ctx, info, true)
	} else {
		progress.Info(ctx, info)
	}
	return true, nil
}

func (s *state) leave(ctx context.Context) error {
	ud := daemon.MustGetUserClient(ctx)
	ctx = progress.WithEventId(ctx, ud.DaemonID().Name)
	progress.Working(ctx, "Ending ingest")
	_, err := ud.LeaveIngest(ctx, &rpc.IngestIdentifier{
		WorkloadName:  s.WorkloadName,
		ContainerName: s.ContainerName,
	})
	if err != nil && grpcStatus.Code(err) == grpcCodes.Canceled {
		// Deactivation was caused by a disconnect
		err = nil
	}
	if err != nil {
		err = grpc.FromGRPC(err)
		err = progress.MaybeWriteError(ctx, err)
	} else {
		progress.Done(ctx, "Ended ingest")
	}
	return err
}

func (s *state) runCommand(ctx context.Context) error {
	// start the interceptor process
	progress.Start(ctx, "Starting")
	defer progress.Stop(ctx)

	ud := daemon.MustGetUserClient(ctx)
	if !s.DockerFlags.Run {
		env := s.info.Environment
		cmd, err := proc.Start(ctx, env, s.Cmdline[0], s.Cmdline[1:]...)
		if err != nil {
			dlog.Errorf(ctx, "error interceptor starting process: %v", err)
			return errcat.NoDaemonLogs.New(err)
		}
		if err = ud.AddHandler(ctx, fmt.Sprintf("%s/%s", s.WorkloadName, s.handlerContainer), cmd, ""); err != nil {
			return err
		}
		// The external command will not output anything to the logs. An error here
		// is likely caused by the user hitting <ctrl>-C to terminate the process.
		return errcat.NoDaemonLogs.New(proc.Wait(ctx, func() {}, cmd))
	}

	ii := NewInfo(ctx, s.info, s.mountError)
	ii.Environment["TELEPRESENCE_INTERCEPT_ID"] = s.WorkloadName + "/" + s.ContainerName
	ii.Environment[agentconfig.EnvAPIHost] = ud.DaemonID().ContainerName()
	dr := cliDocker.Runner{
		Flags:         s.DockerFlags,
		ContainerName: s.handlerContainer,
		Environment:   ii.Environment,
		Mount:         ii.Mount,
	}
	return dr.Run(ctx, s.WaitMessage, s.Cmdline...)
}
