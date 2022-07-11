package userd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/blang/semver"
	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/extensions"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/trafficmgr"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	grpcCodes "google.golang.org/grpc/codes"
	grpcStatus "google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"
	core "k8s.io/api/core/v1"
)

func (s *service) _cmdIntercept(ctx context.Context) *cobra.Command {
	cmd := cobra.Command{
		Use:   "userd-intercept [flags] <intercept_base_name> [-- <command with arguments...>]",
		Short: "Intercept a service",
		Args:  cobra.MinimumNArgs(1),
		Annotations: map[string]string{
			"cobra.commandGroup": "CHANGEME",
		},
	}

	args := interceptArgs{}
	flags := cmd.Flags()

	flags.StringVarP(&args.agentName, "workload", "w", "", "Name of workload (Deployment, ReplicaSet) to intercept, if different from <name>")
	flags.StringVarP(&args.port, "port", "p", strconv.Itoa(client.GetConfig(ctx).Intercept.DefaultPort), ``+
		`Local port to forward to. If intercepting a service with multiple ports, `+
		`use <local port>:<svcPortIdentifier>, where the identifier is the port name or port number. `+
		`With --docker-run, use <local port>:<container port> or <local port>:<container port>:<svcPortIdentifier>.`,
	)

	flags.StringVar(&args.serviceName, "service", "", "Name of service to intercept. If not provided, we will try to auto-detect one")

	flags.BoolVarP(&args.localOnly, "local-only", "l", false, ``+
		`Declare a local-only intercept for the purpose of getting direct outbound access to the intercept's namespace`)

	flags.BoolVarP(&args.previewEnabled, "preview-url", "u", cliutil.HasLoggedIn(ctx), ``+
		`Generate an edgestack.me preview domain for this intercept. `+
		`(default "true" if you are logged in with 'telepresence login', default "false" otherwise)`,
	)
	args.previewSpec = &manager.PreviewSpec{}
	addPreviewFlags("preview-url-", flags, args.previewSpec)

	flags.StringVarP(&args.envFile, "env-file", "e", "", ``+
		`Also emit the remote environment to an env file in Docker Compose format. `+
		`See https://docs.docker.com/compose/env-file/ for more information on the limitations of this format.`)

	flags.StringVarP(&args.envJSON, "env-json", "j", "", `Also emit the remote environment to a file as a JSON blob.`)

	flags.StringVarP(&args.mount, "mount", "", "true", ``+
		`The absolute path for the root directory where volumes will be mounted, $TELEPRESENCE_ROOT. Use "true" to `+
		`have Telepresence pick a random mount point (default). Use "false" to disable filesystem mounting entirely.`)

	flags.StringSliceVar(&args.toPod, "to-pod", []string{}, ``+
		`An additional port to forward from the intercepted pod, will be made available at localhost:PORT `+
		`Use this to, for example, access proxy/helper sidecars in the intercepted pod. The default protocol is TCP. `+
		`Use <port>/UDP for UDP ports`)

	flags.BoolVarP(&args.dockerRun, "docker-run", "", false, ``+
		`Run a Docker container with intercepted environment, volume mount, by passing arguments after -- to 'docker run', `+
		`e.g. '--docker-run -- -it --rm ubuntu:20.04 /bin/bash'`)

	flags.StringVarP(&args.dockerMount, "docker-mount", "", "", ``+
		`The volume mount point in docker. Defaults to same as "--mount"`)

	flags.StringVarP(&args.namespace, "namespace", "n", "", "If present, the namespace scope for this CLI request")

	flags.StringVar(&args.ingressHost, "ingress-host", "", "If this flag is set, the ingress dialogue will be skipped,"+
		" and this value will be used as the ingress hostname.")
	flags.Int32Var(&args.ingressPort, "ingress-port", 0, "If this flag is set, the ingress dialogue will be skipped,"+
		" and this value will be used as the ingress port.")
	flags.BoolVar(&args.ingressTLS, "ingress-tls", false, "If this flag is set, the ingress dialogue will be skipped."+
		" If the dialogue is skipped, this flag will determine if TLS is used, and will default to false.")
	flags.StringVar(&args.ingressL5, "ingress-l5", "", "If this flag is set, the ingress dialogue will be skipped,"+
		" and this value will be used as the L5 hostname. If the dialogue is skipped, this flag will default to the ingress-host value")

	var extErr error
	args.extState, extErr = extensions.LoadExtensions(ctx, flags)

	cmd.RunE = func(cmd *cobra.Command, positional []string) error {
		if extErr != nil {
			return extErr
		}

		// arg-parsing
		var err error
		args.extRequiresLogin, err = args.extState.RequiresAPIKeyOrLicense()
		if err != nil {
			return err
		}
		args.name = positional[0]
		args.cmdline = positional[1:]
		switch args.localOnly { // a switch instead of an if/else to get gocritic to not suggest "else if"
		case true:
			// Not actually intercepting anything -- check that the flags make sense for that
			if args.agentName != "" {
				return errcat.User.New("a local-only intercept cannot have a workload")
			}
			if args.serviceName != "" {
				return errcat.User.New("a local-only intercept cannot have a service")
			}
			if cmd.Flag("port").Changed {
				return errcat.User.New("a local-only intercept cannot have a port")
			}
			if cmd.Flag("mount").Changed {
				return errcat.User.New("a local-only intercept cannot have mounts")
			}
			if cmd.Flag("preview-url").Changed && args.previewEnabled {
				return errcat.User.New("a local-only intercept cannot be previewed")
			}
		case false:
			// Actually intercepting something
			if args.agentName == "" {
				args.agentName = args.name
				if args.namespace != "" {
					args.name += "-" + args.namespace
				}
			}
		}

		args.mountSet = cmd.Flag("mount").Changed
		if args.dockerRun {
			if err := validateDockerArgs(args.cmdline); err != nil {
				return err
			}
		}
		// run
		s.intercept(cmd, args)
		return nil
	}

	return &cmd
}

func (s *service) intercept(cmd *cobra.Command, args interceptArgs) error {
	if len(args.cmdline) == 0 && !args.dockerRun {
		// start and retain the intercept
		return s.withSession(cmd.Context(), "intercept", func(ctx context.Context, ms trafficmgr.Session) error {
			is := newInterceptState(cmd.Context(), safeCobraCommandImpl{cmd}, args, s)
			defer is.scout.Close()
			return client.WithEnsuredState(cmd.Context(), is, true, func() error { return nil })
		})
	}
	return nil
}

type interceptArgs struct {
	name        string // Args[0] || `${Args[0]}-${--namespace}` // which depends on a combinationof --workload and --namespace
	agentName   string // --workload || Args[0] // only valid if !localOnly
	namespace   string // --namespace
	port        string // --port // only valid if !localOnly
	serviceName string // --service // only valid if !localOnly
	localOnly   bool   // --local-only

	previewEnabled bool                 // --preview-url // only valid if !localOnly
	previewSpec    *manager.PreviewSpec // --preview-url-* // only valid if !localOnly

	envFile  string   // --env-file
	envJSON  string   // --env-json
	mount    string   // --mount // "true", "false", or desired mount point // only valid if !localOnly
	mountSet bool     // whether --mount was passed
	toPod    []string // --to-pod

	dockerRun   bool   // --docker-run
	dockerMount string // --docker-mount // where to mount in a docker container. Defaults to mount unless mount is "true" or "false".

	extState         *extensions.ExtensionsState // extension flags
	extRequiresLogin bool                        // pre-extracted from extState

	cmdline []string // Args[1:]

	// ingress cmd inputs
	ingressHost string
	ingressPort int32
	ingressTLS  bool
	ingressL5   string
}

// addPreviewFlags mutates 'flags', adding flags to it such that the flags set the appropriate
// fields in the given 'spec'.  If 'prefix' is given, long-flag names are prefixed with it.
func addPreviewFlags(prefix string, flags *pflag.FlagSet, spec *manager.PreviewSpec) {
	flags.BoolVarP(&spec.DisplayBanner, prefix+"banner", "b", true, "Display banner on preview page")
}

func validateDockerArgs(args []string) error {
	for _, arg := range args {
		if arg == "-d" || arg == "--detach" {
			return errcat.User.New("running docker container in background using -d or --detach is not supported")
		}
	}
	return nil
}

func newInterceptState(
	ctx context.Context,
	cmd safeCobraCommand,
	args interceptArgs,
	s *service,
) *interceptState {
	is := &interceptState{
		cmd:  cmd,
		args: args,

		scout:   scout.NewReporter(ctx, "cli"),
		service: s,
	}
	is.scout.Start(ctx)
	return is
}

// safeCobraCommand is more-or-less a subset of *cobra.Command, with less stuff exposed so I don't
// have to worry about things using it in ways they shouldn't.
type safeCobraCommand interface {
	InOrStdin() io.Reader
	OutOrStdout() io.Writer
	ErrOrStderr() io.Writer
	FlagError(error) error
}

type safeCobraCommandImpl struct {
	*cobra.Command
}

func (w safeCobraCommandImpl) FlagError(err error) error {
	return w.Command.FlagErrorFunc()(w.Command, err)
}

var hostRx = regexp.MustCompile(`^[a-zA-Z1-9](?:[a-zA-Z0-9\-]*[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9\-]*[a-zA-Z0-9])?)*$`)

type interceptState struct {
	// static after newInterceptState() ////////////////////////////////////

	cmd  safeCobraCommand
	args interceptArgs

	scout *scout.Reporter

	service *service

	// set later ///////////////////////////////////////////////////////////

	env        map[string]string
	mountPoint string // if non-empty, this the final mount point of a successful mount
	localPort  uint16 // the parsed <local port>

	dockerPort uint16
}

func makeIngressInfo(ingressHost string, ingressPort int32, ingressTLS bool, ingressL5 string) (*manager.IngressInfo, error) {
	ingress := &manager.IngressInfo{}
	if hostRx.MatchString(ingressHost) {
		if ingressPort > 0 {
			ingress.Host = ingressHost
			ingress.Port = ingressPort
			ingress.UseTls = ingressTLS

			if ingressL5 == "" { // if L5Host is not present
				ingress.L5Host = ingressHost
				return ingress, nil
			} else { // if L5Host is present
				if hostRx.MatchString(ingressL5) {
					ingress.L5Host = ingressL5
					return ingress, nil
				} else {
					return nil, fmt.Errorf("the address provided by --ingress-l5, %s, must match the regex"+
						" [a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)* (e.g. 'myingress.mynamespace')",
						ingressL5)
				}
			}
		} else {
			return nil, fmt.Errorf("the port number provided by --ingress-port, %v, must be a positive integer",
				ingressPort)
		}
	}
	return nil, fmt.Errorf("the address provided by --ingress-host, %s, must match the regex"+
		" [a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)* (e.g. 'myingress.mynamespace')",
		ingressHost)
}

// canInterceptAndLogIn queries the connector if an intercept is possible, and if it is, and if a login is needed,
// also performs a login. This function must be called before other user interaction takes place when creating
// an intercept. It must not be called when no user interaction is expected.
func (is *interceptState) canInterceptAndLogIn(ctx context.Context, ir *connector.CreateInterceptRequest, needLogin bool) error {
	r, err := is.service.CanIntercept(ctx, ir)
	if err != nil {
		return fmt.Errorf("connector.CanIntercept: %w", err)
	}
	if r.Error != connector.InterceptError_UNSPECIFIED {
		return interceptMessage(r)
	}
	if needLogin {
		// We default to assuming they can connect to Ambassador Cloud
		// unless the cluster tells us they can't
		canConnect := true
		mc := is.service.session.ManagerClient()
		if resp, err := mc.CanConnectAmbassadorCloud(ctx, &empty.Empty{}); err == nil {
			// We got a response from the manager; trust that response.
			canConnect = resp.CanConnect
		}
		if canConnect {
			_, err := is.service.Login(ctx, &connector.LoginRequest{})
			if err != nil {
				if grpcStatus.Code(err) == grpcCodes.PermissionDenied {
					err = errcat.User.New(grpcStatus.Convert(err).Message())
				}
				return err
			}
		}
	}
	ir.Spec.WorkloadKind = r.WorkloadKind // Speeds things up slightly when finding the workload next time
	return nil
}

func checkMountCapability(ctx context.Context) error {
	// Use CombinedOutput to include stderr which has information about whether they
	// need to upgrade to a newer version of macFUSE or not
	var cmd *dexec.Cmd
	if runtime.GOOS == "windows" {
		cmd = proc.CommandContext(ctx, "sshfs-win", "cmd", "-V")
	} else {
		cmd = proc.CommandContext(ctx, "sshfs", "-V")
	}
	cmd.DisableLogging = true
	out, err := cmd.CombinedOutput()
	if err != nil {
		dlog.Errorf(ctx, "sshfs not installed: %v", err)
		return errors.New("sshfs is not installed on your local machine")
	}

	// OSXFUSE changed to macFUSE, and we've noticed that older versions of OSXFUSE
	// can cause browsers to hang + kernel crashes, so we add an error to prevent
	// our users from running into this problem.
	// OSXFUSE isn't included in the output of sshfs -V in versions of 4.0.0 so
	// we check for that as a proxy for if they have the right version or not.
	if bytes.Contains(out, []byte("OSXFUSE")) {
		return errors.New(`macFUSE 4.0.5 or higher is required on your local machine`)
	}
	return nil
}

// parsePort parses portSpec based on how it's formatted
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

func (is *interceptState) getMountPoint() (string, bool, error) {
	mountPoint := ""
	doMount, err := strconv.ParseBool(is.args.mount)
	if err != nil {
		mountPoint = is.args.mount
		doMount = len(mountPoint) > 0
		err = nil
	}
	if doMount {
		mountPoint, err = cliutil.PrepareMount(mountPoint)
	}
	return mountPoint, doMount, err
}

func (is *interceptState) createRequest(ctx context.Context) (*connector.CreateInterceptRequest, error) {
	spec := &manager.InterceptSpec{
		Name:      is.args.name,
		Namespace: is.args.namespace,
	}
	ir := &connector.CreateInterceptRequest{Spec: spec}

	if is.args.agentName == "" {
		// local-only
		return ir, nil
	}

	if is.args.serviceName != "" {
		spec.ServiceName = is.args.serviceName
	}

	spec.Agent = is.args.agentName
	spec.TargetHost = "127.0.0.1"

	// Parse port into spec based on how it's formatted
	var err error
	is.localPort, is.dockerPort, spec.ServicePortIdentifier, err = parsePort(is.args.port, is.args.dockerRun)
	if err != nil {
		return nil, err
	}
	spec.TargetPort = int32(is.localPort)

	doMount := false
	if err = checkMountCapability(ctx); err == nil {
		if ir.MountPoint, doMount, err = is.getMountPoint(); err != nil {
			return nil, err
		}
	} else if is.args.mountSet {
		var boolErr error
		doMount, boolErr = strconv.ParseBool(is.args.mount)
		if boolErr != nil || doMount {
			// not --mount=false, so refuse.
			return nil, errcat.User.Newf("remote volume mounts are disabled: %w", err)
		}
	}

	for _, toPod := range is.args.toPod {
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

	if is.args.dockerMount != "" {
		if !is.args.dockerRun {
			return nil, errcat.User.New("--docker-mount must be used together with --docker-run")
		}
		if !doMount {
			return nil, errcat.User.New("--docker-mount cannot be used with --mount=false")
		}
	}

	if spec.Mechanism, err = is.args.extState.Mechanism(); err != nil {
		return nil, err
	}
	if spec.MechanismArgs, err = is.args.extState.MechanismArgs(); err != nil {
		return nil, err
	}
	return ir, nil
}

func (is *interceptState) createAndValidateRequest(ctx context.Context) (*connector.CreateInterceptRequest, error) {
	ir, err := is.createRequest(ctx)
	if err != nil {
		return nil, err
	}

	args := &is.args
	needLogin := !client.GetConfig(ctx).Cloud.SkipLogin && (args.previewEnabled || args.extRequiresLogin)

	// if any of the ingress flags are present, skip the ingress dialogue and use flag values
	if args.previewEnabled {
		spec := args.previewSpec
		if spec.Ingress == nil && (args.ingressHost != "" || args.ingressPort != 0 || args.ingressTLS || args.ingressL5 != "") {
			ingress, err := makeIngressInfo(args.ingressHost, args.ingressPort, args.ingressTLS, args.ingressL5)
			if err != nil {
				return nil, err
			}
			spec.Ingress = ingress
		}
	}
	if needLogin {
		if err := is.canInterceptAndLogIn(ctx, ir, needLogin); err != nil {
			return nil, err
		}
	}

	// The agentImage is only needed if we're dealing with a traffic-manager older than 2.6.0
	mc := is.service.session.ManagerClient()
	vi, err := mc.Version(ctx, &empty.Empty{})
	if err != nil {
		return nil, fmt.Errorf("unable to parse manager.Version: %w", err)
	}
	mv, err := semver.Parse(strings.TrimPrefix(vi.Version, "v"))
	if err != nil {
		return nil, fmt.Errorf("unable to parse manager.Version: %w", err)
	}
	if mv.Major == 2 && mv.Minor < 6 {
		ir.AgentImage, err = is.args.extState.AgentImage(ctx)
		if err != nil {
			return nil, err
		}
	}
	return ir, nil
}

func (is *interceptState) EnsureState(ctx context.Context) (acquired bool, err error) {
	args := &is.args

	// Add whatever metadata we already have to scout
	is.scout.SetMetadatum(ctx, "service_name", args.agentName)
	// TODO(raphaelreyna): find a way to get the cluster id from here
	//is.scout.SetMetadatum(ctx, "cluster_id", is.connInfo.ClusterId)
	mechanism, _ := args.extState.Mechanism()
	mechanismArgs, _ := args.extState.MechanismArgs()
	is.scout.SetMetadatum(ctx, "intercept_mechanism", mechanism)
	is.scout.SetMetadatum(ctx, "intercept_mechanism_numargs", len(mechanismArgs))

	ir, err := is.createAndValidateRequest(ctx)
	if err != nil {
		is.scout.Report(ctx, "intercept_validation_fail", scout.Entry{Key: "error", Value: err.Error()})
		return false, err
	}

	if ir.MountPoint != "" {
		defer func() {
			if !acquired && runtime.GOOS != "windows" {
				// remove if empty
				_ = os.Remove(ir.MountPoint)
			}
		}()
		is.mountPoint = ir.MountPoint
	}

	defer func() {
		if err != nil {
			is.scout.Report(ctx, "intercept_fail", scout.Entry{Key: "error", Value: err.Error()})
		} else {
			is.scout.Report(ctx, "intercept_success")
		}
	}()

	// Submit the request
	r, err := is.service.CreateIntercept(ctx, ir)
	if err != nil {
		return false, fmt.Errorf("connector.CreateIntercept: %w", err)
	}

	if r.Error != connector.InterceptError_UNSPECIFIED {
		if r.GetInterceptInfo().GetDisposition() == manager.InterceptDispositionType_BAD_ARGS {
			_ = is.DeactivateState(ctx)
			return false, is.cmd.FlagError(errcat.User.New(r.InterceptInfo.Message))
		}
		return false, interceptMessage(r)
	}

	if args.agentName == "" {
		// local-only
		return true, nil
	}
	fmt.Fprintf(is.cmd.OutOrStdout(), "Using %s %s\n", r.WorkloadKind, args.agentName)
	var intercept *manager.InterceptInfo

	// Add metadata to scout from InterceptResult
	is.scout.SetMetadatum(ctx, "service_uid", r.GetServiceUid())
	is.scout.SetMetadatum(ctx, "workload_kind", r.GetWorkloadKind())
	// Since a user can create an intercept without specifying a namespace
	// (thus using the default in their kubeconfig), we should be getting
	// the namespace from the InterceptResult because that adds the namespace
	// if it wasn't given on the cli by the user
	is.scout.SetMetadatum(ctx, "service_namespace", r.GetInterceptInfo().GetSpec().GetNamespace())

	if args.previewEnabled {
		if args.previewSpec.Ingress == nil {
			ingressInfo, err := is.service.ResolveIngressInfo(ctx, r.GetServiceProps())
			if err != nil {
				return true, err
			}

			args.previewSpec.Ingress = &manager.IngressInfo{
				Host:   ingressInfo.Host,
				Port:   ingressInfo.Port,
				UseTls: ingressInfo.UseTls,
				L5Host: ingressInfo.L5Host,
			}
		}

		mc := is.service.session.ManagerClient()
		intercept, err = mc.UpdateIntercept(ctx, &manager.UpdateInterceptRequest{
			// TODO(raphaelreyna): Provide the SessionInfo
			// Session: is.connInfo.SessionInfo,
			Name: args.name,
			PreviewDomainAction: &manager.UpdateInterceptRequest_AddPreviewDomain{
				AddPreviewDomain: args.previewSpec,
			},
		})
		if err != nil {
			is.scout.Report(ctx, "preview_domain_create_fail", scout.Entry{Key: "error", Value: err.Error()})
			err = fmt.Errorf("creating preview domain: %w", err)
			return true, err
		}
		if is.env == nil {
			// Some older traffic-managers may return an intercept without an initialized env
			is.env = r.InterceptInfo.Environment
		}

		// MountPoint is not returned by the traffic-manager (of course, it has no idea).
		intercept.ClientMountPoint = r.InterceptInfo.ClientMountPoint
		is.scout.SetMetadatum(ctx, "preview_url", intercept.PreviewDomain)
	} else {
		intercept = r.InterceptInfo
	}
	is.scout.SetMetadatum(ctx, "intercept_id", intercept.Id)

	is.env = intercept.Environment
	is.env["TELEPRESENCE_INTERCEPT_ID"] = intercept.Id
	is.env["TELEPRESENCE_ROOT"] = intercept.ClientMountPoint
	if args.envFile != "" {
		if err = is.writeEnvFile(); err != nil {
			return true, err
		}
	}
	if args.envJSON != "" {
		if err = is.writeEnvJSON(); err != nil {
			return true, err
		}
	}

	var volumeMountProblem error
	doMount, err := strconv.ParseBool(args.mount)
	if doMount || err != nil {
		volumeMountProblem = checkMountCapability(ctx)
	}
	fmt.Fprintln(is.cmd.OutOrStdout(), cliutil.DescribeIntercepts([]*manager.InterceptInfo{intercept}, volumeMountProblem, false))
	return true, nil
}

func (is *interceptState) writeEnvFile() error {
	file, err := os.Create(is.args.envFile)
	if err != nil {
		return errcat.NoDaemonLogs.Newf("failed to create environment file %q: %w", is.args.envFile, err)
	}
	return is.writeEnvToFileAndClose(file)
}

func (is *interceptState) writeEnvToFileAndClose(file *os.File) (err error) {
	defer file.Close()
	w := bufio.NewWriter(file)

	keys := make([]string, len(is.env))
	i := 0
	for k := range is.env {
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
		if _, err = w.WriteString(is.env[k]); err != nil {
			return err
		}
		if err = w.WriteByte('\n'); err != nil {
			return err
		}
	}
	return w.Flush()
}

func (is *interceptState) writeEnvJSON() error {
	data, err := json.MarshalIndent(is.env, "", "  ")
	if err != nil {
		// Creating JSON from a map[string]string should never fail
		panic(err)
	}
	return os.WriteFile(is.args.envJSON, data, 0644)
}

func interceptMessage(r *connector.InterceptResult) error {
	msg := ""
	errCat := errcat.Unknown
	switch r.Error {
	case connector.InterceptError_UNSPECIFIED:
		return nil
	case connector.InterceptError_NO_CONNECTION:
		msg = "Local network is not connected to the cluster"
	case connector.InterceptError_NO_TRAFFIC_MANAGER:
		msg = "Intercept unavailable: no traffic manager"
	case connector.InterceptError_TRAFFIC_MANAGER_CONNECTING:
		msg = "Connecting to traffic manager..."
	case connector.InterceptError_TRAFFIC_MANAGER_ERROR:
		msg = r.ErrorText
	case connector.InterceptError_ALREADY_EXISTS:
		msg = fmt.Sprintf("Intercept with name %q already exists", r.ErrorText)
	case connector.InterceptError_LOCAL_TARGET_IN_USE:
		spec := r.InterceptInfo.Spec
		msg = fmt.Sprintf("Port %s:%d is already in use by intercept %s",
			spec.TargetHost, spec.TargetPort, spec.Name)
	case connector.InterceptError_NO_ACCEPTABLE_WORKLOAD:
		msg = fmt.Sprintf("No interceptable deployment, replicaset, or statefulset matching %s found", r.ErrorText)
	case connector.InterceptError_AMBIGUOUS_MATCH:
		var matches []manager.AgentInfo
		err := json.Unmarshal([]byte(r.ErrorText), &matches)
		if err != nil {
			msg = fmt.Sprintf("Unable to unmarshal JSON: %v", err)
			break
		}
		st := &strings.Builder{}
		fmt.Fprintf(st, "Found more than one possible match:")
		for idx := range matches {
			match := &matches[idx]
			fmt.Fprintf(st, "\n%4d: %s.%s", idx+1, match.Name, match.Namespace)
		}
		msg = st.String()
	case connector.InterceptError_FAILED_TO_ESTABLISH:
		msg = fmt.Sprintf("Failed to establish intercept: %s", r.ErrorText)
	case connector.InterceptError_UNSUPPORTED_WORKLOAD:
		msg = fmt.Sprintf("Unsupported workload type: %s", r.ErrorText)
	case connector.InterceptError_NOT_FOUND:
		msg = fmt.Sprintf("Intercept named %q not found", r.ErrorText)
	case connector.InterceptError_MOUNT_POINT_BUSY:
		msg = fmt.Sprintf("Mount point already in use by intercept %q", r.ErrorText)
	case connector.InterceptError_MISCONFIGURED_WORKLOAD:
		msg = r.ErrorText
	case connector.InterceptError_UNKNOWN_FLAG:
		msg = fmt.Sprintf("Unknown flag: %s", r.ErrorText)
	default:
		msg = fmt.Sprintf("Unknown error code %d", r.Error)
	}
	if r.ErrorCategory > 0 {
		errCat = errcat.Category(r.ErrorCategory)
	}

	if id := r.GetInterceptInfo().GetId(); id != "" {
		msg = fmt.Sprintf("%s: id = %q", msg, id)
	}
	return errCat.Newf(msg)
}

func DescribeIntercepts(iis []*manager.InterceptInfo, volumeMountsPrevented error, debug bool) string {
	sb := strings.Builder{}
	sb.WriteString("intercepted")
	for i, ii := range iis {
		if i > 0 {
			sb.WriteByte('\n')
		}
		describeIntercept(ii, volumeMountsPrevented, debug, &sb)
	}
	return sb.String()
}

func describeIntercept(ii *manager.InterceptInfo, volumeMountsPrevented error, debug bool, sb *strings.Builder) {
	type kv struct {
		Key   string
		Value string
	}

	var fields []kv

	fields = append(fields, kv{"Intercept name", ii.Spec.Name})
	fields = append(fields, kv{"State", func() string {
		msg := ""
		if ii.Disposition > manager.InterceptDispositionType_WAITING {
			msg += "error: "
		}
		msg += ii.Disposition.String()
		if ii.Message != "" {
			msg += ": " + ii.Message
		}
		return msg
	}()})
	fields = append(fields, kv{"Workload kind", ii.Spec.WorkloadKind})

	if debug {
		fields = append(fields, kv{"ID", ii.Id})
	}

	fields = append(fields, kv{"Destination",
		net.JoinHostPort(ii.Spec.TargetHost, fmt.Sprintf("%d", ii.Spec.TargetPort))})

	if ii.Spec.ServicePortIdentifier != "" {
		fields = append(fields, kv{"Service Port Identifier", ii.Spec.ServicePortIdentifier})
	}
	if debug {
		fields = append(fields, kv{"Mechanism", ii.Spec.Mechanism})
		fields = append(fields, kv{"Mechanism Args", fmt.Sprintf("%q", ii.Spec.MechanismArgs)})
		fields = append(fields, kv{"Metadata", fmt.Sprintf("%q", ii.Metadata)})
	}

	if ii.ClientMountPoint != "" {
		fields = append(fields, kv{"Volume Mount Point", ii.ClientMountPoint})
	} else if volumeMountsPrevented != nil {
		fields = append(fields, kv{"Volume Mount Error", volumeMountsPrevented.Error()})
	}

	fields = append(fields, kv{"Intercepting", func() string {
		if ii.MechanismArgsDesc == "" {
			if len(ii.Spec.MechanismArgs) > 0 {
				return fmt.Sprintf("using mechanism=%q with args=%q", ii.Spec.Mechanism, ii.Spec.MechanismArgs)
			}
			return fmt.Sprintf("using mechanism=%q", ii.Spec.Mechanism)
		}
		return ii.MechanismArgsDesc
	}()})

	if ii.PreviewDomain != "" {
		previewURL := ii.PreviewDomain
		// Right now SystemA gives back domains with the leading "https://", but
		// let's not rely on that.
		if !strings.HasPrefix(previewURL, "https://") && !strings.HasPrefix(previewURL, "http://") {
			previewURL = "https://" + previewURL
		}
		fields = append(fields, kv{"Preview URL", previewURL})
	}
	if l5Hostname := ii.GetPreviewSpec().GetIngress().GetL5Host(); l5Hostname != "" {
		fields = append(fields, kv{"Layer 5 Hostname", l5Hostname})
	}

	klen := 0
	for _, kv := range fields {
		if len(kv.Key) > klen {
			klen = len(kv.Key)
		}
	}
	for _, kv := range fields {
		vlines := strings.Split(strings.TrimSpace(kv.Value), "\n")
		fmt.Fprintf(sb, "\n    %-*s: %s", klen, kv.Key, vlines[0])
		for _, vline := range vlines[1:] {
			sb.WriteString("\n      ")
			sb.WriteString(vline)
		}
	}
}

func (is *interceptState) DeactivateState(ctx context.Context) error {
	return removeIntercept(ctx, strings.TrimSpace(is.args.name))
}

func removeIntercept(ctx context.Context, name string) error {
	return cliutil.WithStartedConnector(ctx, true, func(ctx context.Context, connectorClient connector.ConnectorClient) error {
		var r *connector.InterceptResult
		var err error
		r, err = connectorClient.RemoveIntercept(dcontext.WithoutCancel(ctx), &manager.RemoveInterceptRequest2{Name: name})
		if err != nil {
			return err
		}
		if r.Error != connector.InterceptError_UNSPECIFIED {
			return interceptMessage(r)
		}
		return nil
	})
}
