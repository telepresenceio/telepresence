package intercept

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/pkg/errors"
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
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/util"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/shellquote"
)

type State struct {
	*Args

	Cmd        *cobra.Command
	Scout      *scout.Reporter
	Env        map[string]string
	MountPoint string // if non-empty, this the final mount point of a successful mount
	LocalPort  uint16 // the parsed <local port>
	DockerPort uint16
}

func NewState(
	cmd *cobra.Command,
	args *Args,
) *State {
	return &State{
		Args:  args,
		Cmd:   cmd,
		Scout: scout.NewReporter(cmd.Context(), "cli"),
	}
}

func (s *State) Intercept() error {
	ctx := s.Cmd.Context()
	s.Scout.Start(ctx)
	defer s.Scout.Close()

	if len(s.Cmdline) == 0 && !s.DockerRun {
		// start and retain the intercept
		return client.WithEnsuredState(ctx, s, true, func() error { return nil })
	}

	return client.WithEnsuredState(ctx, s, false, func() (err error) {
		// start the interceptor process

		var cmd *dexec.Cmd
		if s.DockerRun {
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
			cmd, err = s.startInDocker(ctx, envFile, s.Cmdline)
		} else {
			cmd, err = proc.Start(ctx, s.Env, s.Cmd, s.Cmdline[0], s.Cmdline[1:]...)
		}
		if err != nil {
			dlog.Errorf(ctx, "error interceptor starting process: %v", err)
			return errcat.NoDaemonLogs.New(err)
		}

		// setup cleanup for the interceptor process
		ior := connector.Interceptor{
			InterceptId: s.Env["TELEPRESENCE_INTERCEPT_ID"],
			Pid:         int32(cmd.Process.Pid),
		}

		// Send info about the pid and intercept id to the traffic-manager so that it kills
		// the process if it receives a leave of quit call.
		if _, err = util.GetUserDaemon(ctx).AddInterceptor(ctx, &ior); err != nil {
			if grpcStatus.Code(err) == grpcCodes.Canceled {
				// Deactivation was caused by a disconnect
				err = nil
			}
			dlog.Errorf(ctx, "error adding process with pid %d as interceptor: %v", ior.Pid, err)
			_ = cmd.Process.Kill()
			return err
		}

		// The external command will not output anything to the logs. An error here
		// is likely caused by the user hitting <ctrl>-C to terminate the process.
		return errcat.NoDaemonLogs.New(proc.Wait(ctx, func() {}, cmd))
	})
}

func (s *State) EnsureState(ctx context.Context) (acquired bool, err error) {
	ud := util.GetUserDaemon(ctx)
	status, err := ud.Status(ctx, &empty.Empty{})
	if err != nil {
		return false, err
	}

	// Add whatever metadata we already have to scout
	s.Scout.SetMetadatum(ctx, "service_name", s.AgentName)
	s.Scout.SetMetadatum(ctx, "cluster_id", status.ClusterId)
	s.Scout.SetMetadatum(ctx, "intercept_mechanism", s.Mechanism)
	s.Scout.SetMetadatum(ctx, "intercept_mechanism_numargs", len(s.MechanismArgs))

	ir, err := s.CreateRequest(ctx)
	if err != nil {
		s.Scout.Report(ctx, "intercept_validation_fail", scout.Entry{Key: "error", Value: err.Error()})
		return false, err
	}

	if ir.MountPoint != "" {
		defer func() {
			if !acquired && runtime.GOOS != "windows" {
				// remove if empty
				_ = os.Remove(ir.MountPoint)
			}
		}()
		s.MountPoint = ir.MountPoint
	}

	defer func() {
		if err != nil {
			s.Scout.Report(ctx, "intercept_fail", scout.Entry{Key: "error", Value: err.Error()})
		} else {
			s.Scout.Report(ctx, "intercept_success")
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
	fmt.Fprintf(s.Cmd.OutOrStdout(), "Using %s %s\n", r.WorkloadKind, s.AgentName)
	var intercept *manager.InterceptInfo

	// Add metadata to scout from InterceptResult
	s.Scout.SetMetadatum(ctx, "service_uid", r.GetServiceUid())
	s.Scout.SetMetadatum(ctx, "workload_kind", r.GetWorkloadKind())
	// Since a user can create an intercept without specifying a namespace
	// (thus using the default in their kubeconfig), we should be getting
	// the namespace from the InterceptResult because that adds the namespace
	// if it wasn't given on the cli by the user
	s.Scout.SetMetadatum(ctx, "service_namespace", r.GetInterceptInfo().GetSpec().GetNamespace())
	intercept = r.InterceptInfo
	s.Scout.SetMetadatum(ctx, "intercept_id", intercept.Id)

	s.Env = intercept.Environment
	if s.Env == nil {
		s.Env = make(map[string]string)
	}
	s.Env["TELEPRESENCE_INTERCEPT_ID"] = intercept.Id
	s.Env["TELEPRESENCE_ROOT"] = intercept.ClientMountPoint
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
	doMount, err := strconv.ParseBool(s.Mount)
	if doMount || err != nil {
		volumeMountProblem = s.CheckMountCapability(ctx)
	}
	fmt.Fprintln(s.Cmd.OutOrStdout(), util.DescribeIntercepts([]*manager.InterceptInfo{intercept}, volumeMountProblem, false))
	return true, nil
}

func (s *State) DeactivateState(ctx context.Context) error {
	r, err := util.GetUserDaemon(ctx).RemoveIntercept(ctx, &manager.RemoveInterceptRequest2{Name: strings.TrimSpace(s.Name)})
	if err != nil && grpcStatus.Code(err) == grpcCodes.Canceled {
		// Deactivation was caused by a disconnect
		err = nil
	}
	return Result(r, err)
}

func (s *State) CheckMountCapability(ctx context.Context) error {
	r, err := util.GetUserDaemon(ctx).RemoteMountAvailability(ctx, &empty.Empty{})
	if err != nil {
		return err
	}
	return errcat.FromResult(r)
}

func (s *State) CreateRequest(ctx context.Context) (*connector.CreateInterceptRequest, error) {
	spec := &manager.InterceptSpec{
		Name:      s.Name,
		Namespace: s.Namespace,
	}
	ir := &connector.CreateInterceptRequest{Spec: spec}

	if s.AgentName == "" {
		// local-only
		return ir, nil
	}

	if s.ServiceName != "" {
		spec.ServiceName = s.ServiceName
	}

	spec.Mechanism = s.Mechanism
	spec.MechanismArgs = s.MechanismArgs
	spec.Agent = s.AgentName
	spec.TargetHost = "127.0.0.1"

	// Parse port into spec based on how it's formatted
	var err error
	s.LocalPort, s.DockerPort, spec.ServicePortIdentifier, err = parsePort(s.Port, s.DockerRun)
	if err != nil {
		return nil, err
	}
	spec.TargetPort = int32(s.LocalPort)

	doMount := false
	if err = s.CheckMountCapability(ctx); err == nil {
		if ir.MountPoint, doMount, err = s.GetMountPoint(ctx); err != nil {
			return nil, err
		}
	} else if s.MountSet {
		var boolErr error
		doMount, boolErr = strconv.ParseBool(s.Mount)
		if boolErr != nil || doMount {
			// not --mount=false, so refuse.
			return nil, errcat.User.Newf("remote volume mounts are disabled: %w", err)
		}
	}

	for _, toPod := range s.ToPod {
		pp, err := agentconfig.NewPortAndProto(toPod)
		if err != nil {
			return nil, errcat.User.New(err)
		}
		spec.LocalPorts = append(spec.LocalPorts, pp.String())
		if pp.Proto == core.ProtocolTCP {
			// For backward compatibility
			spec.ExtraPorts = append(spec.ExtraPorts, int32(pp.Port))
		}
	}

	if s.DockerMount != "" {
		if !s.DockerRun {
			return nil, errcat.User.New("--docker-mount must be used together with --docker-run")
		}
		if !doMount {
			return nil, errcat.User.New("--docker-mount cannot be used with --mount=false")
		}
	}
	return ir, nil
}

func (s *State) startInDocker(ctx context.Context, envFile string, args []string) (*dexec.Cmd, error) {
	ourArgs := []string{
		"run",
		"--env-file", envFile,
		"--dns-search", "tel2-search",
	}

	getArg := func(s string) (string, bool) {
		for i, arg := range args {
			if strings.Contains(arg, s) {
				parts := strings.Split(arg, "=")
				if len(parts) == 2 {
					return parts[1], true
				}
				if i+1 < len(args) {
					return parts[i+1], true
				}
				return "", true
			}
		}
		return "", false
	}

	name, hasName := getArg("--name")
	if hasName && name == "" {
		return nil, errors.New("no value found for docker flag `--name`")
	}
	if !hasName {
		name = fmt.Sprintf("intercept-%s-%d", s.Name, s.LocalPort)
		ourArgs = append(ourArgs, "--name", name)
	}

	if s.DockerPort != 0 {
		ourArgs = append(ourArgs, "-p", fmt.Sprintf("%d:%d", s.LocalPort, s.DockerPort))
	}

	dockerMount := ""
	if s.MountPoint != "" { // do we have a mount point at all?
		if dockerMount = s.DockerMount; dockerMount == "" {
			dockerMount = s.MountPoint
		}
	}
	if dockerMount != "" {
		ourArgs = append(ourArgs, "-v", fmt.Sprintf("%s:%s", s.MountPoint, dockerMount))
	}
	args = append(ourArgs, args...)
	cmd := proc.CommandContext(ctx, "docker", args...)
	cmd.DisableLogging = true
	cmd.Stdout = s.Cmd.OutOrStdout()
	cmd.Stderr = s.Cmd.ErrOrStderr()
	cmd.Stdin = s.Cmd.InOrStdin()
	dlog.Debugf(ctx, shellquote.ShellString("docker", args))
	err := cmd.Start()
	if err != nil {
		return nil, err
	}
	return cmd, err
}

func (s *State) writeEnvFile() error {
	file, err := os.Create(s.EnvFile)
	if err != nil {
		return errcat.NoDaemonLogs.Newf("failed to create environment file %q: %w", s.EnvFile, err)
	}
	return s.writeEnvToFileAndClose(file)
}

func (s *State) writeEnvToFileAndClose(file *os.File) (err error) {
	defer file.Close()
	w := bufio.NewWriter(file)

	keys := make([]string, len(s.Env))
	i := 0
	for k := range s.Env {
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
		if _, err = w.WriteString(s.Env[k]); err != nil {
			return err
		}
		if err = w.WriteByte('\n'); err != nil {
			return err
		}
	}
	return w.Flush()
}

func (s *State) writeEnvJSON() error {
	data, err := json.MarshalIndent(s.Env, "", "  ")
	if err != nil {
		// Creating JSON from a map[string]string should never fail
		panic(err)
	}
	return os.WriteFile(s.EnvJSON, data, 0o644)
}

// parsePort parses portSpec based on how it's formatted.
func parsePort(portSpec string, dockerRun bool) (local uint16, docker uint16, svcPortId string, err error) {
	portMapping := strings.Split(portSpec, ":")
	portError := func() (uint16, uint16, string, error) {
		if dockerRun {
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
		if dockerRun {
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
	if dockerRun && docker == 0 {
		docker = local
	}
	return local, docker, svcPortId, nil
}
