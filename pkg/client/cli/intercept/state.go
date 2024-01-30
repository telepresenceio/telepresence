package intercept

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	grpcCodes "google.golang.org/grpc/codes"
	grpcStatus "google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"
	core "k8s.io/api/core/v1"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/dnet"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

type State interface {
	Cmd() *cobra.Command
	CreateRequest(context.Context) (*connector.CreateInterceptRequest, error)
	Name() string
	Run(context.Context) error
	RunAndLeave() bool
}

type state struct {
	*Command
	cmd           *cobra.Command
	env           map[string]string
	mountDisabled bool
	mountPoint    string // if non-empty, this the final mount point of a successful mount
	localPort     uint16 // the parsed <local port>
	dockerPort    uint16
	status        *connector.ConnectInfo
	info          *Info // Info from the created intercept

	// Possibly extended version of the state. Use when calling interface methods.
	self State
}

func NewState(
	cmd *cobra.Command,
	args *Command,
) State {
	s := &state{
		Command: args,
		cmd:     cmd,
	}
	s.self = s
	return s
}

func (s *state) SetSelf(self State) {
	s.self = self
}

func (s *state) Cmd() *cobra.Command {
	return s.cmd
}

func (s *state) CreateRequest(ctx context.Context) (*connector.CreateInterceptRequest, error) {
	spec := &manager.InterceptSpec{
		Name:    s.Name(),
		Replace: s.Replace,
	}
	ir := &connector.CreateInterceptRequest{
		Spec:         spec,
		ExtendedInfo: s.ExtendedInfo,
	}

	if s.AgentName == "" {
		// local-only
		s.mountDisabled = true
		return ir, nil
	}

	if s.ServiceName != "" {
		spec.ServiceName = s.ServiceName
	}

	spec.Mechanism = s.Mechanism
	spec.MechanismArgs = s.MechanismArgs
	spec.Agent = s.AgentName
	spec.TargetHost = "127.0.0.1"

	ud := daemon.GetUserClient(ctx)

	// Parse port into spec based on how it's formatted
	var err error
	s.localPort, s.dockerPort, spec.ServicePortIdentifier, err = parsePort(s.Port, s.DockerRun, ud.Containerized())
	if err != nil {
		return nil, err
	}
	spec.TargetPort = int32(s.localPort)
	if iputil.Parse(s.Address) == nil {
		return nil, fmt.Errorf("--address %s is not a valid IP address", s.Address)
	}
	spec.TargetHost = s.Address

	mountEnabled, mountPoint := s.GetMountPoint()
	if !mountEnabled {
		s.mountDisabled = true
	} else {
		if ud.Containerized() && ir.LocalMountPort == 0 {
			// No use having the remote container actually mount, so let's have it create a bridge
			// to the remote sftp server instead.
			lma, err := dnet.FreePortsTCP(1)
			if err != nil {
				return nil, err
			}
			s.LocalMountPort = uint16(lma[0].Port)
			mountPoint = ""
		}

		if err = s.checkMountCapability(ctx); err != nil {
			err = fmt.Errorf("remote volume mounts are disabled: %w", err)
			if mountPoint != "" {
				return nil, err
			}
			// Log a warning and disable, but continue
			s.mountDisabled = true
			dlog.Warning(ctx, err)
		}

		if !s.mountDisabled {
			ir.LocalMountPort = int32(s.LocalMountPort)
			if ir.LocalMountPort == 0 {
				var cwd string
				if cwd, err = os.Getwd(); err != nil {
					return nil, err
				}
				if ir.MountPoint, err = PrepareMount(cwd, mountPoint); err != nil {
					return nil, err
				}
			}
		}
	}

	for _, toPod := range s.ToPod {
		pp, err := agentconfig.NewPortAndProto(toPod)
		if err != nil {
			return nil, err
		}
		spec.LocalPorts = append(spec.LocalPorts, pp.String())
		if pp.Proto == core.ProtocolTCP {
			// For backward compatibility
			spec.ExtraPorts = append(spec.ExtraPorts, int32(pp.Port))
		}
	}

	if s.DockerMount != "" {
		if !s.DockerRun {
			return nil, errors.New("--docker-mount must be used together with --docker-run")
		}
		if s.mountDisabled {
			return nil, errors.New("--docker-mount cannot be used with --mount=false")
		}
	}
	return ir, nil
}

func (s *state) Name() string {
	return s.Command.Name
}

func (s *state) RunAndLeave() bool {
	return len(s.Cmdline) > 0 || s.DockerRun
}

func (s *state) Run(ctx context.Context) error {
	ctx = scout.NewReporter(ctx, "cli")
	scout.Start(ctx)
	defer scout.Close(ctx)

	if !s.RunAndLeave() {
		// start and retain the intercept
		return client.WithEnsuredState(ctx, s.create, nil, nil)
	}

	// start intercept, run command, then leave the intercept
	if s.DockerRun {
		if err := s.prepareDockerRun(docker.EnableClient(ctx)); err != nil {
			return err
		}
	}
	return client.WithEnsuredState(ctx, s.create, s.runCommand, s.leave)
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
	scout.SetMetadatum(ctx, "cluster_id", s.status.ClusterId)
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
		s.mountPoint = ir.MountPoint
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

	if s.AgentName == "" {
		// local-only
		return true, nil
	}
	detailedOutput := s.DetailedOutput && output.WantsFormatted(s.cmd)
	if !detailedOutput {
		fmt.Fprintf(s.cmd.OutOrStdout(), "Using %s %s\n", r.WorkloadKind, s.AgentName)
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
	if s.EnvFile != "" {
		if err = s.writeEnvFile(); err != nil {
			return true, err
		}
	}
	if s.EnvJSON != "" {
		if err = s.writeEnvJSON(); err != nil {
			return true, err
		}
	}

	var volumeMountProblem error
	if ir.LocalMountPort != 0 {
		intercept.PodIp = "127.0.0.1"
		intercept.SftpPort = ir.LocalMountPort
	} else {
		doMount, err := strconv.ParseBool(s.Mount)
		if doMount || err != nil {
			volumeMountProblem = s.checkMountCapability(ctx)
		}
	}
	mountError := ""
	if volumeMountProblem != nil {
		mountError = volumeMountProblem.Error()
	}
	s.info = NewInfo(ctx, intercept, mountError)
	if detailedOutput {
		output.Object(ctx, s.info, true)
	} else {
		out := s.cmd.OutOrStdout()
		_, _ = s.info.WriteTo(out)
		_, _ = fmt.Fprintln(out)
	}
	return true, nil
}

func (s *state) leave(ctx context.Context) error {
	r, err := daemon.GetUserClient(ctx).RemoveIntercept(ctx, &manager.RemoveInterceptRequest2{Name: strings.TrimSpace(s.Name())})
	if err != nil && grpcStatus.Code(err) == grpcCodes.Canceled {
		// Deactivation was caused by a disconnect
		err = nil
	}
	return Result(r, err)
}

func (s *state) runCommand(ctx context.Context) error {
	// start the interceptor process
	ctx = dos.WithStdio(ctx, s.cmd)
	ud := daemon.GetUserClient(ctx)
	if !s.DockerRun {
		cmd, err := proc.Start(ctx, s.env, s.Cmdline[0], s.Cmdline[1:]...)
		if err != nil {
			dlog.Errorf(ctx, "error interceptor starting process: %v", err)
			return errcat.NoDaemonLogs.New(err)
		}
		if cmd == nil {
			return nil
		}
		if err = s.addInterceptorToDaemon(ctx, cmd, ""); err != nil {
			return err
		}

		// The external command will not output anything to the logs. An error here
		// is likely caused by the user hitting <ctrl>-C to terminate the process.
		return errcat.NoDaemonLogs.New(proc.Wait(ctx, func() {}, cmd))
	}

	envFile := s.EnvFile
	if envFile == "" {
		file, err := os.CreateTemp("", "tel-*.env")
		if err != nil {
			return fmt.Errorf("failed to create temporary environment file. %w", err)
		}
		defer os.Remove(file.Name())

		if err = s.writeEnvToFileAndClose(file); err != nil {
			return err
		}
		envFile = file.Name()
	}

	// Ensure that the intercept handler is stopped properly if the daemon quits
	procCtx, cancel := context.WithCancel(ctx)
	go func() {
		if err := daemon.CancelWhenRmFromCache(procCtx, cancel, ud.DaemonID.InfoFileName()); err != nil {
			dlog.Error(ctx)
		}
	}()
	dr := s.startInDocker(ctx, envFile, s.Cmdline)
	if dr.err == nil {
		dr.err = s.addInterceptorToDaemon(ctx, dr.cmd, dr.name)
	}
	return dr.wait(procCtx)
}

func (s *state) addInterceptorToDaemon(ctx context.Context, cmd *dexec.Cmd, containerName string) error {
	// setup cleanup for the interceptor process
	ior := connector.Interceptor{
		InterceptId:   s.env["TELEPRESENCE_INTERCEPT_ID"],
		Pid:           int32(cmd.Process.Pid),
		ContainerName: containerName,
	}

	// Send info about the pid and intercept id to the traffic-manager so that it kills
	// the process if it receives a leave of quit call.
	if _, err := daemon.GetUserClient(ctx).AddInterceptor(ctx, &ior); err != nil {
		if grpcStatus.Code(err) == grpcCodes.Canceled {
			// Deactivation was caused by a disconnect
			err = nil
		} else {
			dlog.Errorf(ctx, "error adding process with pid %d as interceptor: %v", ior.Pid, err)
		}
		_ = cmd.Process.Kill()
		return err
	}
	return nil
}

func (s *state) checkMountCapability(ctx context.Context) error {
	r, err := daemon.GetUserClient(ctx).RemoteMountAvailability(ctx, &empty.Empty{})
	if err != nil {
		return err
	}
	return errcat.FromResult(r)
}

func (s *state) writeEnvFile() error {
	file, err := os.Create(s.EnvFile)
	if err != nil {
		return errcat.NoDaemonLogs.Newf("failed to create environment file %q: %w", s.EnvFile, err)
	}
	return s.writeEnvToFileAndClose(file)
}

func (s *state) writeEnvToFileAndClose(file *os.File) (err error) {
	defer file.Close()
	w := bufio.NewWriter(file)

	keys := make([]string, len(s.env))
	i := 0
	for k := range s.env {
		keys[i] = k
		i++
	}
	sort.Strings(keys)

	for _, k := range keys {
		if _, err = w.WriteString(k); err != nil {
			return err
		}
		if err = w.WriteByte('='); err != nil {
			return err
		}
		if _, err = w.WriteString(s.env[k]); err != nil {
			return err
		}
		if err = w.WriteByte('\n'); err != nil {
			return err
		}
	}
	return w.Flush()
}

func (s *state) writeEnvJSON() error {
	data, err := json.MarshalIndent(s.env, "", "  ")
	if err != nil {
		// Creating JSON from a map[string]string should never fail
		panic(err)
	}
	return os.WriteFile(s.EnvJSON, data, 0o644)
}

// parsePort parses portSpec based on how it's formatted.
func parsePort(portSpec string, dockerRun, remote bool) (local uint16, docker uint16, svcPortId string, err error) {
	portMapping := strings.Split(portSpec, ":")
	portError := func() (uint16, uint16, string, error) {
		if dockerRun && !remote {
			return 0, 0, "", errcat.User.New("port must be of the format --port <local-port>:<container-port>[:<svcPortIdentifier>]")
		}
		return 0, 0, "", errcat.User.New("port must be of the format --port <local-port>[:<svcPortIdentifier>]")
	}

	if local, err = agentconfig.ParseNumericPort(portMapping[0]); err != nil {
		return portError()
	}

	switch len(portMapping) {
	case 1:
	case 2:
		p := portMapping[1]
		if dockerRun && !remote {
			if docker, err = agentconfig.ParseNumericPort(p); err != nil {
				return portError()
			}
		} else {
			if err := agentconfig.ValidatePort(p); err != nil {
				return portError()
			}
			svcPortId = p
		}
	case 3:
		if remote && dockerRun {
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
	if dockerRun && !remote && docker == 0 {
		docker = local
	}
	return local, docker, svcPortId, nil
}
