package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dexec"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/extensions"
)

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

type interceptState struct {
	// static after newInterceptState() ////////////////////////////////////

	cmd  safeCobraCommand
	args interceptArgs

	Scout *client.Scout

	connectorClient connector.ConnectorClient
	managerClient   manager.ManagerClient
	connInfo        *connector.ConnectInfo

	// set later ///////////////////////////////////////////////////////////

	env        map[string]string
	mountPoint string // if non-empty, this the final mount point of a successful mount
	localPort  uint16 // the parsed <local port>

	dockerPort uint16
}

func interceptCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:  "intercept [flags] <intercept_base_name> [-- <command with arguments...>]",
		Args: cobra.MinimumNArgs(1),

		Short:   "Intercept a service",
		PreRunE: updateCheckIfDue,
	}
	args := interceptArgs{}
	flags := cmd.Flags()

	flags.StringVarP(&args.agentName, "workload", "w", "", "Name of workload (Deployment, ReplicaSet) to intercept, if different from <name>")
	flags.StringVarP(&args.port, "port", "p", "8080", ``+
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
		`Use this to, for example, access proxy/helper sidecars in the intercepted pod.`)

	flags.BoolVarP(&args.dockerRun, "docker-run", "", false, ``+
		`Run a Docker container with intercepted environment, volume mount, by passing arguments after -- to 'docker run', `+
		`e.g. '--docker-run -- -it --rm ubuntu:20.04 /bin/bash'`)

	flags.StringVarP(&args.dockerMount, "docker-mount", "", "", ``+
		`The volume mount point in docker. Defaults to same as "--mount"`)

	flags.StringVarP(&args.namespace, "namespace", "n", "", "If present, the namespace scope for this CLI request")

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
		if args.localOnly {
			// Not actually intercepting anything -- check that the flags make sense for that
			if args.agentName != "" {
				return errors.New("a local-only intercept cannot have a workload")
			}
			if args.serviceName != "" {
				return errors.New("a local-only intercept cannot have a service")
			}
			if cmd.Flag("port").Changed {
				return errors.New("a local-only intercept cannot have a port")
			}
			if cmd.Flag("mount").Changed {
				return errors.New("a local-only intercept cannot have mounts")
			}
			if cmd.Flag("preview-url").Changed && args.previewEnabled {
				return errors.New("a local-only intercept cannot be previewed")
			}
		} else { //nolint:gocritic
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
		return intercept(cmd, args)
	}

	return cmd
}

func leaveCommand() *cobra.Command {
	return &cobra.Command{
		Use:  "leave [flags] <intercept_name>",
		Args: cobra.ExactArgs(1),

		Short: "Remove existing intercept",
		RunE: func(cmd *cobra.Command, args []string) error {
			return removeIntercept(cmd.Context(), strings.TrimSpace(args[0]))
		},
	}
}

// Checks if login is necessary and then takes the necessary actions
// depending if the cluster can connect to Ambassador Cloud
func loginIfNeeded(ctx context.Context, args interceptArgs) error {
	if !client.GetConfig(ctx).Cloud.SkipLogin && (args.previewEnabled || args.extRequiresLogin) {
		return cliutil.WithConnector(ctx, func(ctx context.Context, _ connector.ConnectorClient) error {
			return cliutil.WithManager(ctx, func(ctx context.Context, managerClient manager.ManagerClient) error {
				// We default to assuming they can connect to Ambassador Cloud
				// unless the cluster tells us they can't
				canConnect := true
				if resp, err := managerClient.CanConnectAmbassadorCloud(ctx, &empty.Empty{}); err == nil {
					// We got a response from the manager; trust that response.
					canConnect = resp.CanConnect
				}
				if canConnect {
					if _, err := cliutil.EnsureLoggedIn(ctx, ""); err != nil {
						return err
					}
				}
				return nil
			})
		})
	}
	return nil
}

func intercept(cmd *cobra.Command, args interceptArgs) error {
	if len(args.cmdline) == 0 && !args.dockerRun {
		// start and retain the intercept
		return withConnector(cmd, true, func(ctx context.Context, connectorClient connector.ConnectorClient, connInfo *connector.ConnectInfo) error {
			if err := loginIfNeeded(ctx, args); err != nil {
				return err
			}
			return cliutil.WithManager(ctx, func(ctx context.Context, managerClient manager.ManagerClient) error {
				is := newInterceptState(ctx, safeCobraCommandImpl{cmd}, args, connectorClient, managerClient, connInfo)
				return client.WithEnsuredState(ctx, is, true, func() error { return nil })
			})
		})
	}

	// start intercept, run command, then stop the intercept
	return withConnector(cmd, false, func(ctx context.Context, connectorClient connector.ConnectorClient, connInfo *connector.ConnectInfo) error {
		if err := loginIfNeeded(ctx, args); err != nil {
			return err
		}
		return cliutil.WithManager(ctx, func(ctx context.Context, managerClient manager.ManagerClient) error {
			is := newInterceptState(ctx, safeCobraCommandImpl{cmd}, args, connectorClient, managerClient, connInfo)
			return client.WithEnsuredState(ctx, is, false, func() error {
				if args.dockerRun {
					return is.runInDocker(ctx, is.cmd, args.cmdline)
				}
				return start(ctx, args.cmdline[0], args.cmdline[1:], true,
					cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(),
					envPairs(is.env)...)
			})
		})
	})
}

func newInterceptState(
	ctx context.Context,
	cmd safeCobraCommand,
	args interceptArgs,
	connectorClient connector.ConnectorClient,
	managerClient manager.ManagerClient,
	connInfo *connector.ConnectInfo,
) *interceptState {
	return &interceptState{
		cmd:  cmd,
		args: args,

		Scout: client.NewScout(ctx, "cli"),

		connectorClient: connectorClient,
		managerClient:   managerClient,
		connInfo:        connInfo,
	}
}

func interceptMessage(r *connector.InterceptResult) string {
	msg := ""
	switch r.Error {
	case connector.InterceptError_UNSPECIFIED:
		msg = "No error"
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
			spec.TargetHost, spec.TargetPort, r.ErrorText)
	case connector.InterceptError_NO_ACCEPTABLE_WORKLOAD:
		msg = fmt.Sprintf("No interceptable deployment or replicaset matching %s found", r.ErrorText)
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
	case connector.InterceptError_NOT_FOUND:
		msg = fmt.Sprintf("Intercept named %q not found", r.ErrorText)
	case connector.InterceptError_MOUNT_POINT_BUSY:
		msg = fmt.Sprintf("Mount point already in use by intercept %q", r.ErrorText)
	default:
		msg = fmt.Sprintf("Unknown error code %d", r.Error)
	}
	if id := r.GetInterceptInfo().GetId(); id != "" {
		return fmt.Sprintf("Intercept %q: %s", id, msg)
	}
	return fmt.Sprintf("Intercept: %s", msg)
}

func checkMountCapability(ctx context.Context) error {
	// Use CombinedOutput to include stderr which has information about whether they
	// need to upgrade to a newer version of macFUSE or not
	cmd := dexec.CommandContext(ctx, "sshfs", "-V")
	cmd.DisableLogging = true
	out, err := cmd.CombinedOutput()
	if err != nil {
		return errors.New("sshfs is not installed on your local machine")
	}

	// OSXFUSE changed to macFUSE and we've noticed that older versions of OSXFUSE
	// can cause browsers to hang + kernel crashes, so we add an error to prevent
	// our users from running into this problem.
	// OSXFUSE isn't included in the output of sshfs -V in versions of 4.0.0 so
	// we check for that as a proxy for if they have the right version or not.
	if bytes.Contains(out, []byte("OSXFUSE")) {
		return errors.New(`macFUSE 4.0.5 or higher is required on your local machine`)
	}
	return nil
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
	portMapping := strings.Split(is.args.port, ":")
	portError := func() error {
		if is.args.dockerRun {
			return errors.New("ports must be of the format --ports <local-port>:<container-port>[:<svcPortIdentifier>]")
		}
		return errors.New("ports must be of the format --ports <local-port>[:<svcPortIdentifier>]")
	}

	parsePort := func(portStr string) (uint16, error) {
		port, err := strconv.ParseUint(portStr, 10, 16)
		if err != nil {
			return 0, fmt.Errorf("port numbers must be a valid, positive int, you gave: %q", is.args.port)
		}
		return uint16(port), nil
	}

	port, err := parsePort(portMapping[0])
	if err != nil {
		return nil, err
	}
	is.localPort = port
	spec.TargetPort = int32(port)

	switch len(portMapping) {
	case 1:
	case 2:
		if port, err = parsePort(portMapping[1]); err == nil && is.args.dockerRun {
			is.dockerPort = port
		} else {
			spec.ServicePortIdentifier = portMapping[1]
		}
	case 3:
		if !is.args.dockerRun {
			return nil, portError()
		}
		if port, err = parsePort(portMapping[1]); err != nil {
			return nil, err
		}
		is.dockerPort = port
		spec.ServicePortIdentifier = portMapping[2]
	default:
		return nil, portError()
	}

	if is.args.dockerRun && is.dockerPort == 0 {
		is.dockerPort = is.localPort
	}

	doMount := false
	err = checkMountCapability(ctx)
	if err == nil {
		mountPoint := ""
		doMount, err = strconv.ParseBool(is.args.mount)
		if err != nil {
			mountPoint = is.args.mount
			doMount = len(mountPoint) > 0
		}

		if doMount {
			if mountPoint == "" {
				if mountPoint, err = ioutil.TempDir("", "telfs-"); err != nil {
					return nil, err
				}
			} else {
				if err = os.MkdirAll(mountPoint, 0700); err != nil {
					return nil, err
				}
			}
			ir.MountPoint = mountPoint
		}
	} else if is.args.mountSet {
		var boolErr error
		doMount, boolErr = strconv.ParseBool(is.args.mount)
		if boolErr != nil || doMount {
			// not --mount=false, so refuse.
			return nil, fmt.Errorf("remote volume mounts are disabled: %w", err)
		}
	}

	for _, toPod := range is.args.toPod {
		port, err := parsePort(toPod)
		if err != nil {
			return nil, fmt.Errorf("Unable to parse port %s: %w", toPod, err)
		}
		spec.ExtraPorts = append(spec.ExtraPorts, int32(port))
	}

	if is.args.dockerMount != "" {
		if !is.args.dockerRun {
			return nil, errors.New("--docker-mount must be used together with --docker-run")
		}
		if !doMount {
			return nil, errors.New("--docker-mount cannot be used with --mount=false")
		}
	}

	spec.Mechanism, err = is.args.extState.Mechanism()
	if err != nil {
		return nil, err
	}
	spec.MechanismArgs, err = is.args.extState.MechanismArgs()
	if err != nil {
		return nil, err
	}

	var env client.Env
	env, err = client.LoadEnv(ctx)
	if err != nil {
		return nil, err
	}
	ir.AgentImage, err = is.args.extState.AgentImage(ctx, env)
	if err != nil {
		return nil, err
	}
	return ir, nil
}

func (is *interceptState) EnsureState(ctx context.Context) (acquired bool, err error) {
	// Fill defaults
	if is.args.previewEnabled && is.args.previewSpec.Ingress == nil {
		ingress, err := selectIngress(ctx, is.cmd.InOrStdin(), is.cmd.OutOrStdout(), is.connInfo)
		if err != nil {
			return false, err
		}
		is.args.previewSpec.Ingress = ingress
	}

	ir, err := is.createRequest(ctx)
	if err != nil {
		return false, err
	}

	if ir.MountPoint != "" {
		defer func() {
			if !acquired {
				// remove if empty
				_ = os.Remove(ir.MountPoint)
			}
		}()
		is.mountPoint = ir.MountPoint
	}

	// Submit the request
	r, err := is.connectorClient.CreateIntercept(ctx, ir)
	if err != nil {
		return false, fmt.Errorf("connector.CreateIntercept: %w", err)
	}

	switch r.Error {
	case connector.InterceptError_UNSPECIFIED:
		if is.args.agentName == "" {
			// local-only
			return true, nil
		}
		fmt.Fprintf(is.cmd.OutOrStdout(), "Using %s %s\n", r.WorkloadKind, is.args.agentName)
		var intercept *manager.InterceptInfo

		// Add metadata to scout from InterceptResult
		is.Scout.SetMetadatum("service_uid", r.GetServiceUid())
		is.Scout.SetMetadatum("workload_kind", r.GetWorkloadKind())
		// Since a user can create an intercept without specifying a namespace
		// (thus using the default in their kubeconfig), we should be getting
		// the namespace from the InterceptResult because that adds the namespace
		// if it wasn't given on the cli by the user
		is.Scout.SetMetadatum("service_namespace", r.GetInterceptInfo().GetSpec().GetNamespace())

		// Add metadata to scout
		is.Scout.SetMetadatum("service_name", is.args.agentName)
		is.Scout.SetMetadatum("cluster_id", is.connInfo.ClusterId)

		mechanism, _ := is.args.extState.Mechanism()
		mechanismArgs, _ := is.args.extState.MechanismArgs()
		is.Scout.SetMetadatum("intercept_mechanism", mechanism)
		is.Scout.SetMetadatum("intercept_mechanism_numargs", len(mechanismArgs))

		if is.args.previewEnabled {
			intercept, err = is.managerClient.UpdateIntercept(ctx, &manager.UpdateInterceptRequest{
				Session: is.connInfo.SessionInfo,
				Name:    is.args.name,
				PreviewDomainAction: &manager.UpdateInterceptRequest_AddPreviewDomain{
					AddPreviewDomain: is.args.previewSpec,
				},
			})
			if err != nil {
				_ = is.Scout.Report(ctx, "preview_domain_create_fail", client.ScoutMeta{Key: "error", Value: err.Error()})
				err = fmt.Errorf("creating preview domain: %w", err)
				return true, err
			}
			is.Scout.SetMetadatum("preview_url", intercept.PreviewDomain)
		} else {
			intercept = r.InterceptInfo
		}
		is.Scout.SetMetadatum("intercept_id", intercept.Id)

		is.env = r.Environment
		if is.args.envFile != "" {
			if err = is.writeEnvFile(); err != nil {
				return true, err
			}
		}
		if is.args.envJSON != "" {
			if err = is.writeEnvJSON(); err != nil {
				return true, err
			}
		}

		var volumeMountProblem error
		doMount, err := strconv.ParseBool(is.args.mount)
		if doMount || err != nil {
			volumeMountProblem = checkMountCapability(ctx)
		}
		fmt.Fprintln(is.cmd.OutOrStdout(), DescribeIntercept(intercept, volumeMountProblem, false))
		_ = is.Scout.Report(ctx, "intercept_success")
		return true, nil
	case connector.InterceptError_ALREADY_EXISTS:
		fmt.Fprintln(is.cmd.OutOrStdout(), interceptMessage(r))
		return false, nil
	default:
		if r.GetInterceptInfo().GetDisposition() == manager.InterceptDispositionType_BAD_ARGS {
			_ = is.DeactivateState(ctx)
			_ = is.cmd.FlagError(errors.New(r.InterceptInfo.Message))
			panic("not reached; FlagErrorFunc should call os.Exit()")
		}
		return false, errors.New(interceptMessage(r))
	}
}

func (is *interceptState) DeactivateState(ctx context.Context) error {
	return removeIntercept(ctx, strings.TrimSpace(is.args.name))
}

func removeIntercept(ctx context.Context, name string) error {
	return cliutil.WithStartedConnector(ctx, func(ctx context.Context, connectorClient connector.ConnectorClient) error {
		var r *connector.InterceptResult
		var err error
		r, err = connectorClient.RemoveIntercept(dcontext.WithoutCancel(ctx), &manager.RemoveInterceptRequest2{Name: name})
		if err != nil {
			return err
		}
		if r.Error != connector.InterceptError_UNSPECIFIED {
			return errors.New(interceptMessage(r))
		}
		return nil
	})
}

func validateDockerArgs(args []string) error {
	for _, arg := range args {
		if arg == "-d" || arg == "--detach" {
			return errors.New("running docker container in background using -d or --detach is not supported")
		}
	}
	return nil
}

func (is *interceptState) runInDocker(ctx context.Context, cmd safeCobraCommand, args []string) error {
	envFile := is.args.envFile
	if envFile == "" {
		file, err := ioutil.TempFile("", "tel-*.env")
		if err != nil {
			return fmt.Errorf("failed to create temporary environment file. %w", err)
		}
		defer os.Remove(file.Name())

		if err = is.writeEnvToFileAndClose(file); err != nil {
			return err
		}
		envFile = file.Name()
	}

	ourArgs := []string{
		"run",
		"--dns-search", "tel2-search",
		"--env-file", envFile,
	}
	hasArg := func(s string) bool {
		for _, arg := range args {
			if s == arg {
				return true
			}
		}
		return false
	}
	if !hasArg("--name") {
		ourArgs = append(ourArgs, "--name", fmt.Sprintf("intercept-%s-%d", is.args.name, is.localPort))
	}

	if is.dockerPort != 0 {
		ourArgs = append(ourArgs, "-p", fmt.Sprintf("%d:%d", is.localPort, is.dockerPort))
	}

	dockerMount := ""
	if is.mountPoint != "" { // do we have a mount point at all?
		if dockerMount = is.args.dockerMount; dockerMount == "" {
			dockerMount = is.mountPoint
		}
	}
	if dockerMount != "" {
		ourArgs = append(ourArgs, "-v", fmt.Sprintf("%s:%s", is.mountPoint, dockerMount))
	}
	return start(ctx, "docker", append(ourArgs, args...), true, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
}

func (is *interceptState) writeEnvFile() error {
	file, err := os.Create(is.args.envFile)
	if err != nil {
		return fmt.Errorf("failed to create environment file %q: %w", is.args.envFile, err)
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
	return ioutil.WriteFile(is.args.envJSON, data, 0644)
}

var hostRx = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9\-]*[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9\-]*[a-zA-Z0-9])?)*$`)

const (
	ingressDesc = `To create a preview URL, telepresence needs to know how cluster
ingress works for this service.  Please %s the ingress to use.`
	ingressQ1 = `1/4: What's your ingress' layer 3 (IP) address?
     You may use an IP address or a DNS name (this is usually a
     "service.namespace" DNS name).`
	ingressQ2 = `2/4: What's your ingress' layer 4 address (TCP port number)?`
	ingressQ3 = `3/4: Does that TCP port on your ingress use TLS (as opposed to cleartext)?`
	ingressQ4 = `4/4: If required by your ingress, specify a different layer 5 hostname
     (TLS-SNI, HTTP "Host" header) to access this service.`
)

func showPrompt(out io.Writer, question string, defaultValue interface{}) {
	if reflect.ValueOf(defaultValue).IsZero() {
		fmt.Fprintf(out, "\n%s\n\n       [no default]: ", question)
	} else {
		fmt.Fprintf(out, "\n%s\n\n       [default: %v]: ", question, defaultValue)
	}
}

func askForHost(question, cachedHost string, reader *bufio.Reader, out io.Writer) (string, error) {
	for {
		showPrompt(out, question, cachedHost)
		reply, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		reply = strings.TrimSpace(reply)
		if reply == "" {
			if cachedHost == "" {
				continue
			}
			return cachedHost, nil
		}
		if hostRx.MatchString(reply) {
			return reply, nil
		}
		fmt.Fprintf(out,
			"Address %q must match the regex [a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)* (e.g. 'myingress.mynamespace')\n",
			reply)
	}
}

func askForPortNumber(cachedPort int32, reader *bufio.Reader, out io.Writer) (int32, error) {
	for {
		showPrompt(out, ingressQ2, cachedPort)
		reply, err := reader.ReadString('\n')
		if err != nil {
			return 0, err
		}
		reply = strings.TrimSpace(reply)
		if reply == "" {
			if cachedPort == 0 {
				continue
			}
			return cachedPort, nil
		}
		port, err := strconv.Atoi(reply)
		if err == nil && port > 0 {
			return int32(port), nil
		}
		fmt.Fprintln(out, "port must be a positive integer")
	}
}

func askForUseTLS(cachedUseTLS bool, reader *bufio.Reader, out io.Writer) (bool, error) {
	yn := "n"
	if cachedUseTLS {
		yn = "y"
	}
	showPrompt(out, ingressQ3, yn)
	for {
		reply, err := reader.ReadString('\n')
		if err != nil {
			return false, err
		}
		switch strings.TrimSpace(reply) {
		case "":
			return cachedUseTLS, nil
		case "n", "N":
			return false, nil
		case "y", "Y":
			return true, nil
		}
		fmt.Fprintf(out, "       please answer 'y' or 'n'\n       [default: %v]: ", yn)
	}
}

func selectIngress(ctx context.Context, in io.Reader, out io.Writer, connInfo *connector.ConnectInfo) (*manager.IngressInfo, error) {
	infos, err := cache.LoadIngressesFromUserCache(ctx)
	if err != nil {
		return nil, err
	}
	key := connInfo.ClusterServer + "/" + connInfo.ClusterContext
	selectOrConfirm := "Confirm"
	cachedIngressInfo := infos[key]
	if cachedIngressInfo == nil {
		iis := connInfo.IngressInfos
		if len(iis) > 0 {
			cachedIngressInfo = iis[0] // TODO: Better handling when there are several alternatives. Perhaps use SystemA for this?
		} else {
			selectOrConfirm = "Select" // Hard to confirm unless there's a default.
			cachedIngressInfo = &manager.IngressInfo{}
		}
	}

	reader := bufio.NewReader(in)

	fmt.Fprintf(out, "\n"+ingressDesc+"\n", selectOrConfirm)
	reply := &manager.IngressInfo{}
	if reply.Host, err = askForHost(ingressQ1, cachedIngressInfo.Host, reader, out); err != nil {
		return nil, err
	}
	if reply.Port, err = askForPortNumber(cachedIngressInfo.Port, reader, out); err != nil {
		return nil, err
	}
	if reply.UseTls, err = askForUseTLS(cachedIngressInfo.UseTls, reader, out); err != nil {
		return nil, err
	}
	if cachedIngressInfo.L5Host == "" {
		cachedIngressInfo.L5Host = reply.Host
	}
	if reply.L5Host, err = askForHost(ingressQ4, cachedIngressInfo.L5Host, reader, out); err != nil {
		return nil, err
	}
	fmt.Fprintln(out)

	if !ingressInfoEqual(cachedIngressInfo, reply) {
		infos[key] = reply
		if err = cache.SaveIngressesToUserCache(ctx, infos); err != nil {
			return nil, err
		}
	}
	return reply, nil
}

func ingressInfoEqual(a, b *manager.IngressInfo) bool {
	return a.Host == b.Host && a.L5Host == b.L5Host && a.Port == b.Port && a.UseTls == b.UseTls
}
