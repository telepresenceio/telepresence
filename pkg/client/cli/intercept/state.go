package intercept

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strings"

	grpcCodes "google.golang.org/grpc/codes"
	grpcStatus "google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	cliDocker "github.com/telepresenceio/telepresence/v2/pkg/client/cli/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/progress"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

type State interface {
	CreateRequest(context.Context) (*connector.CreateInterceptRequest, error)
	Name() string
	Run(context.Context) (*Info, error)
	RunAndLeave() bool
}

type state struct {
	*Command
	env              map[string]string
	localPort        uint16 // the parsed <local port>
	dockerPort       uint16
	status           *connector.ConnectInfo
	info             *Info // Info from the created intercept
	mountError       error
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

func (s *state) CreateRequest(ctx context.Context) (*connector.CreateInterceptRequest, error) {
	spec := &manager.InterceptSpec{
		Name:    s.Name(),
		Replace: s.Replace,
	}
	ir := &connector.CreateInterceptRequest{
		Spec:           spec,
		ExtendedInfo:   s.ExtendedInfo,
		LocalMountPort: int32(s.MountFlags.LocalMountPort),
		MountPoint:     s.MountFlags.Mount,
		MountReadOnly:  s.MountFlags.ReadOnly,
	}

	spec.ServiceName = s.ServiceName
	spec.ContainerName = s.ContainerName
	spec.Mechanism = s.Mechanism
	spec.MechanismArgs = s.MechanismArgs
	spec.Wiretap = s.Wiretap
	spec.Agent = s.AgentName
	spec.NoDefaultPort = s.NoDefaultPort

	// HTTP Intercepts: populate header filters
	if len(s.HTTPHeaderFilters) > 0 {
		spec.HeaderFilters = make(map[string]string, len(s.HTTPHeaderFilters))
		for _, header := range s.HTTPHeaderFilters {
			if key, value, err := parseHTTPHeader(header); err == nil {
				spec.HeaderFilters[key] = value
			}
			// Note: parseHTTPHeader errors are already caught in validation,
			// so we can safely ignore them here
		}
	}
	// Combine all path filters with their type prefixes
	var allPathFilters []string

	// Add exact match filters
	for _, path := range s.HTTPPathEqualFilters {
		allPathFilters = append(allPathFilters, ":path-equal:"+path)
	}

	// Add prefix match filters
	for _, path := range s.HTTPPathPrefixFilters {
		allPathFilters = append(allPathFilters, ":path-prefix:"+path)
	}

	// Add regex match filters
	for _, path := range s.HTTPPathRegexFilters {
		allPathFilters = append(allPathFilters, ":path-regex:"+path)
	}

	spec.PathFilters = allPathFilters

	for _, toPod := range s.ToPod {
		pp, err := types.ParsePortAndProto(toPod)
		if err != nil {
			return nil, err
		}
		spec.LocalPorts = append(spec.LocalPorts, pp.String())
	}

	ud := daemon.MustGetUserClient(ctx)

	// Parse port into spec based on how it's formatted
	s.localPort, s.dockerPort, spec.PortIdentifier = 0, 0, ""
	if len(s.Ports) > 0 {
		var err error
		s.localPort, s.dockerPort, spec.PortIdentifier, err = parsePort(s.Ports[0], s.DockerFlags.Run, ud.Containerized())
		if err != nil {
			return nil, err
		}
		for i := 1; i < len(s.Ports); i++ {
			pm := s.Ports[i]
			if colIdx := strings.IndexByte(pm, ':'); colIdx > 0 {
				// The "--port" arg puts local port first, but it's the destination in the pod-port mapping.
				to := pm[:colIdx]
				from := pm[colIdx+1:]
				if slashIdx := strings.IndexByte(from, '/'); slashIdx > 0 {
					from = from[:slashIdx]
					to += from[slashIdx:]
				}
				pm = from + ":" + to
			}
			if err = types.PortMapping(pm).Validate(); err != nil {
				return nil, errcat.User.New(err)
			}
			spec.PodPorts = append(spec.PodPorts, pm)
		}
	}

	spec.TargetPort = int32(s.localPort)
	switch {
	case s.Address != "":
		spec.TargetHost = s.Address
	case ud.Containerized() && s.handlerContainer != "":
		// The name will be translated into a synthetic IP that will be registered with
		// the daemon. The daemon will reverse the translation when dialing the local
		// target and use DNS to find the container IP.
		spec.TargetHost = s.handlerContainer
	default:
		spec.TargetHost = "127.0.0.1"
	}
	return ir, nil
}

func (s *state) Name() string {
	return s.Command.Name
}

func (s *state) RunAndLeave() bool {
	return len(s.Cmdline) > 0 || s.DockerFlags.Run
}

func (s *state) Run(ctx context.Context) (*Info, error) {
	progress.Start(ctx, "Initializing")
	defer progress.Stop(ctx)

	var err error
	if !s.RunAndLeave() {
		err = client.WithEnsuredState(ctx, s.create, nil, nil)
		if err != nil {
			return nil, err
		}
		return s.info, nil
	}

	// start intercept, run command, then leave the intercept
	if s.DockerFlags.Run {
		ctx = docker.EnableClient(ctx)
		err = s.DockerFlags.PullOrBuildImage(progress.WithEventId(ctx, "Handler"))
		if err != nil {
			return nil, err
		}
		defaultContainerName := fmt.Sprintf("%s-%s-%d", s.what(), s.Name(), s.localPort)
		s.handlerContainer, s.Cmdline, err = s.DockerFlags.GetContainerNameAndArgs(defaultContainerName)
		if err != nil {
			return nil, err
		}
		if s.handlerContainer != defaultContainerName {
			// Check if the given name is already in use.
			ud := daemon.MustGetSession(ctx)
			ip, err := ud.Lookup(ctx, s.handlerContainer)
			if err == nil {
				// We're about to start a container with a name that is already present in the cluster. That's
				// probably a mistake.
				progress.Warningf(ctx, "the container name %q will override the current mapping to IP %s", s.handlerContainer, ip)
			}
		}
	}
	err = client.WithEnsuredState(ctx, s.create, s.runCommand, s.leave)
	if err != nil {
		return nil, err
	}
	return s.info, nil
}

func (s *state) what() string {
	what := "intercept"
	if s.Wiretap {
		what = "wiretap"
	} else if s.NoDefaultPort {
		what = "replace"
	}
	return what
}

func (s *state) create(ctx context.Context) (acquired bool, err error) {
	ud := daemon.MustGetUserClient(ctx)
	s.status, err = ud.Status(ctx, &empty.Empty{})
	if err != nil {
		return false, err
	}

	progress.Start(ctx, "Creating")
	defer progress.Stop(ctx)

	ir, err := s.self.CreateRequest(ctx)
	if err != nil {
		return false, errcat.NoDaemonLogs.New(err)
	}

	// Submit the request
	egType := types.EngagementTypeFromSpec(ir.Spec)
	progress.Working(ctx, egType.Working())
	r, err := ud.CreateIntercept(ctx, ir)
	if err = Result(r, err); err != nil {
		return false, progress.MaybeWriteError(ctx, fmt.Errorf("connector.CreateIntercept: %w", err))
	}
	progress.Done(ctx, egType.WorkDone())
	progress.Infof(ctx, "Using %s %s", r.WorkloadKind, s.AgentName)

	// Since a user can create an intercept without specifying a namespace
	// (thus using the default in their kubeconfig), we should be getting
	// the namespace from the InterceptResult because that adds the namespace
	// if it wasn't given on the cli by the user
	intercept := r.InterceptInfo

	s.env = intercept.Environment
	if s.env == nil {
		s.env = make(map[string]string)
	}
	s.env["TELEPRESENCE_INTERCEPT_ID"] = intercept.Id
	s.env["TELEPRESENCE_ROOT"] = intercept.ClientMountPoint
	if err = s.EnvFlags.MaybeWrite(s.env); err != nil {
		return true, err
	}

	if s.MountFlags.Enabled {
		if ir.LocalMountPort != 0 {
			intercept.PodIp = "127.0.0.1"
			intercept.SftpPort = ir.LocalMountPort
		}
	} else {
		intercept.MountPoint = ""
		intercept.FtpPort = 0
		intercept.SftpPort = 0
	}

	s.info = NewInfo(ctx, intercept, s.MountFlags.ReadOnly, s.mountError)
	detailedOutput := s.DetailedOutput && s.FormattedOutput
	if detailedOutput {
		output.Object(ctx, s.info, true)
	} else {
		progress.Info(ctx, s.info)
	}
	return true, nil
}

func (s *state) leave(ctx context.Context) error {
	progress.Start(ctx, "Leaving")
	m := s.info.Mount
	if m != nil && m.LocalDir != "" {
		defer func() {
			if runtime.GOOS != "windows" {
				// remove if empty
				_ = os.Remove(m.LocalDir)
			}
		}()
	}
	n := strings.TrimSpace(s.Name())
	ud := daemon.MustGetUserClient(ctx)
	progress.Workingf(ctx, "Ending %s", s.what())
	r, err := ud.RemoveIntercept(ctx, &manager.RemoveInterceptRequest2{Name: n})
	if err != nil && grpcStatus.Code(err) == grpcCodes.Canceled {
		// Deactivation was caused by a disconnect
		err = nil
	}
	if err != nil {
		err = progress.MaybeWriteError(ctx, err)
	} else {
		progress.Donef(ctx, "Ended %s", s.what())
	}
	return Result(r, err)
}

func (s *state) runCommand(ctx context.Context) error {
	// start the interceptor process
	progress.Start(ctx, "Starting")
	defer progress.Stop(ctx)

	if !s.DockerFlags.Run {
		env := s.info.Environment
		cmd, err := proc.Start(ctx, env, s.Cmdline[0], s.Cmdline[1:]...)
		if err != nil {
			dlog.Errorf(ctx, "error interceptor starting process: %v", err)
			return errcat.NoDaemonLogs.New(err)
		}
		if err = daemon.MustGetUserClient(ctx).AddHandler(ctx, env["TELEPRESENCE_INTERCEPT_ID"], cmd, ""); err != nil {
			return err
		}
		// The external command will not output anything to the logs. An error here
		// is likely caused by the user hitting <ctrl>-C to terminate the process.
		return errcat.NoDaemonLogs.New(proc.Wait(ctx, func() {}, cmd))
	}

	dr := cliDocker.Runner{
		Flags:         s.DockerFlags,
		ContainerName: s.handlerContainer,
		Environment:   s.info.Environment,
		Mount:         s.info.Mount,
	}
	if s.dockerPort != 0 {
		s.Cmdline = slices.Insert(s.Cmdline, 0, "-p", fmt.Sprintf("%d:%d", s.localPort, s.dockerPort))
		dr.AdjustImageIndex(2)
	}
	return dr.Run(ctx, s.WaitMessage, s.Cmdline...)
}

// parsePort parses portSpec based on how it's formatted.
func parsePort(portSpec string, dockerRun, containerized bool) (local uint16, docker uint16, svcPortId string, err error) {
	if portSpec == "" {
		return 0, 0, "", nil
	}
	portMapping := strings.Split(portSpec, ":")
	portError := func() (uint16, uint16, string, error) {
		if dockerRun && !containerized {
			return 0, 0, "", errcat.User.Newf("port must be of the format --port <local-port>:<container-port>[:<svcPortIdentifier>], was %q", portSpec)
		}
		return 0, 0, "", errcat.User.Newf("port must be of the format --port <local-port>[:<svcPortIdentifier>], was %q", portSpec)
	}

	if p := portMapping[0]; p != "" {
		if local, err = types.ParsePort(p); err != nil {
			return portError()
		}
	}

	switch len(portMapping) {
	case 1:
	case 2:
		if p := portMapping[1]; p != "" {
			if p == "all" {
				return 0, 0, p, nil
			}
			if dockerRun && !containerized {
				if docker, err = types.ParsePort(p); err != nil {
					return portError()
				}
			} else {
				if err := types.ValidatePort(p); err != nil {
					return portError()
				}
				svcPortId = p
			}
		}
	case 3:
		if containerized && dockerRun {
			return 0, 0, "", errcat.User.New(
				"the format --port <local-port>:<container-port>:<svcPortIdentifier> cannot be used when the daemon runs in a container")
		}
		if !dockerRun {
			return portError()
		}
		if docker, err = types.ParsePort(portMapping[1]); err != nil {
			return portError()
		}
		svcPortId = portMapping[2]
		if err := types.ValidatePort(svcPortId); err != nil {
			return portError()
		}
	default:
		return portError()
	}
	if dockerRun && !containerized && docker == 0 {
		docker = local
	}
	return local, docker, svcPortId, nil
}
