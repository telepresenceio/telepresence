package intercept

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"runtime"
	"strings"

	grpcCodes "google.golang.org/grpc/codes"
	grpcStatus "google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	cliDocker "github.com/telepresenceio/telepresence/v2/pkg/client/cli/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
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
	spec.Agent = s.AgentName
	spec.TargetHost = "127.0.0.1"

	ud := daemon.GetUserClient(ctx)

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
			if err = agentconfig.PortMapping(pm).Validate(); err != nil {
				return nil, errcat.User.New(err)
			}
			spec.PodPorts = append(spec.PodPorts, pm)
		}
	}

	spec.TargetPort = int32(s.localPort)
	if _, err := netip.ParseAddr(s.Address); err != nil {
		return nil, fmt.Errorf("--address %s is not a valid IP address", s.Address)
	}
	spec.TargetHost = s.Address

	for _, toPod := range s.ToPod {
		pp, err := agentconfig.NewPortAndProto(toPod)
		if err != nil {
			return nil, err
		}
		spec.LocalPorts = append(spec.LocalPorts, pp.String())
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
	ctx = scout.NewReporter(ctx, "cli")
	scout.Start(ctx)
	defer scout.Close(ctx)

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
		err = s.DockerFlags.PullOrBuildImage(ctx)
		if err != nil {
			return nil, err
		}
		s.handlerContainer, s.Cmdline, err = s.DockerFlags.GetContainerNameAndArgs(fmt.Sprintf("intercept-%s-%d", s.Name(), s.localPort))
		if err != nil {
			return nil, err
		}
	}
	err = client.WithEnsuredState(ctx, s.create, s.runCommand, s.leave)
	if err != nil {
		return nil, err
	}
	return s.info, nil
}

func (s *state) create(ctx context.Context) (acquired bool, err error) {
	ud := daemon.GetUserClient(ctx)
	s.status, err = ud.Status(ctx, &empty.Empty{})
	if err != nil {
		return false, err
	}

	// Add whatever metadata we already have to scout
	scout.SetMetadatum(ctx, "service_name", s.AgentName)
	scout.SetMetadatum(ctx, "manager_install_id", s.status.ManagerInstallId)
	scout.SetMetadatum(ctx, "intercept_mechanism", s.Mechanism)
	scout.SetMetadatum(ctx, "intercept_mechanism_numargs", len(s.MechanismArgs))

	ir, err := s.self.CreateRequest(ctx)
	if err != nil {
		scout.Report(ctx, "intercept_validation_fail", scout.Entry{Key: "error", Value: err.Error()})
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

	defer func() {
		if err != nil {
			scout.Report(ctx, "intercept_fail", scout.Entry{Key: "error", Value: err.Error()})
		} else {
			scout.Report(ctx, "intercept_success")
		}
	}()

	// Submit the request
	r, err := ud.CreateIntercept(ctx, ir)
	if err = Result(r, err); err != nil {
		return false, fmt.Errorf("connector.CreateIntercept: %w", err)
	}
	if s.EnvFlags.File == "-" {
		s.Silent = true
	}
	detailedOutput := s.DetailedOutput && s.FormattedOutput
	if !s.Silent && !detailedOutput {
		ioutil.Printf(dos.Stdout(ctx), "Using %s %s\n", r.WorkloadKind, s.AgentName)
	}
	var intercept *manager.InterceptInfo

	// Add metadata to scout from InterceptResult
	scout.SetMetadatum(ctx, "service_uid", r.GetServiceUid())
	scout.SetMetadatum(ctx, "workload_kind", r.GetWorkloadKind())
	// Since a user can create an intercept without specifying a namespace
	// (thus using the default in their kubeconfig), we should be getting
	// the namespace from the InterceptResult because that adds the namespace
	// if it wasn't given on the cli by the user
	scout.SetMetadatum(ctx, "service_namespace", r.GetInterceptInfo().GetSpec().GetNamespace())
	intercept = r.InterceptInfo
	scout.SetMetadatum(ctx, "intercept_id", intercept.Id)

	s.env = intercept.Environment
	if s.env == nil {
		s.env = make(map[string]string)
	}
	s.env["TELEPRESENCE_INTERCEPT_ID"] = intercept.Id
	s.env["TELEPRESENCE_ROOT"] = intercept.ClientMountPoint
	if err = s.EnvFlags.PerhapsWrite(s.env); err != nil {
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
	if !s.Silent {
		if detailedOutput {
			output.Object(ctx, s.info, true)
		} else {
			out := dos.Stdout(ctx)
			_, _ = s.info.WriteTo(out)
			_, _ = fmt.Fprintln(out)
		}
	}
	return true, nil
}

func (s *state) leave(ctx context.Context) error {
	n := strings.TrimSpace(s.Name())
	dlog.Debugf(ctx, "Leaving intercept %s", n)
	r, err := daemon.GetUserClient(ctx).RemoveIntercept(ctx, &manager.RemoveInterceptRequest2{Name: n})
	if err != nil && grpcStatus.Code(err) == grpcCodes.Canceled {
		// Deactivation was caused by a disconnect
		err = nil
	}
	if err != nil {
		dlog.Errorf(ctx, "Leaving intercept ended with error %v", err)
	}
	return Result(r, err)
}

func (s *state) runCommand(ctx context.Context) error {
	// start the interceptor process
	if !s.DockerFlags.Run {
		env := s.info.Environment
		cmd, err := proc.Start(ctx, env, s.Cmdline[0], s.Cmdline[1:]...)
		if err != nil {
			dlog.Errorf(ctx, "error interceptor starting process: %v", err)
			return errcat.NoDaemonLogs.New(err)
		}
		if err = daemon.GetUserClient(ctx).AddHandler(ctx, env["TELEPRESENCE_INTERCEPT_ID"], cmd, ""); err != nil {
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
		dr.Flags.PublishedPorts = append(dr.Flags.PublishedPorts, cliDocker.PublishedPort{
			HostAddrPort:  netip.AddrPortFrom(netip.IPv4Unspecified(), s.localPort),
			Protocol:      "tcp",
			ContainerPort: s.dockerPort,
		})
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
			return 0, 0, "", errcat.User.New("port must be of the format --port <local-port>:<container-port>[:<svcPortIdentifier>]")
		}
		return 0, 0, "", errcat.User.New("port must be of the format --port <local-port>[:<svcPortIdentifier>]")
	}

	if p := portMapping[0]; p != "" {
		if local, err = agentconfig.ParseNumericPort(p); err != nil {
			return portError()
		}
	}

	switch len(portMapping) {
	case 1:
	case 2:
		if p := portMapping[1]; p != "" {
			if dockerRun && !containerized {
				if docker, err = agentconfig.ParseNumericPort(p); err != nil {
					return portError()
				}
			} else {
				if err := agentconfig.ValidatePort(p); err != nil {
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
		if docker, err = agentconfig.ParseNumericPort(portMapping[1]); err != nil {
			return portError()
		}
		svcPortId = portMapping[2]
		if err := agentconfig.ValidatePort(svcPortId); err != nil {
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
