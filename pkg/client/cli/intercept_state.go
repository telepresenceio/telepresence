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

	"github.com/datawire/dlib/dexec"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/extensions"
)

type interceptInfo struct {
	sessionInfo

	name        string
	agentName   string
	namespace   string
	port        string
	serviceName string
	localOnly   bool

	previewEnabled bool
	previewSpec    manager.PreviewSpec

	envFile string
	envJSON string
	mount   string // "true", "false", or desired mount point
	toPod   []string

	dockerRun   bool
	dockerMount string // where to mount in a docker container. Defaults to mount unless mount is "true" or "false".

	extState *extensions.ExtensionsState
	extErr   error
}

type interceptState struct {
	*interceptInfo
	cs         *connectorState
	Scout      *client.Scout
	env        map[string]string
	mountPoint string // if non-empty, this the final mount point of a successful mount
	localPort  uint16 // the parsed <local port>

	dockerPort uint16
}

func interceptCommand(ctx context.Context) *cobra.Command {
	ii := &interceptInfo{}
	cmd := &cobra.Command{
		Use:  "intercept [flags] <intercept_base_name> [-- <command with arguments...>]",
		Args: cobra.MinimumNArgs(1),

		Short:   "Intercept a service",
		RunE:    ii.intercept,
		PreRunE: updateCheckIfDue,
	}
	flags := cmd.Flags()

	flags.StringVarP(&ii.agentName, "workload", "w", "", "Name of workload (Deployment, ReplicaSet) to intercept, if different from <name>")
	flags.StringVarP(&ii.port, "port", "p", "8080", ``+
		`Local port to forward to. If intercepting a service with multiple ports, `+
		`use <local port>:<svcPortIdentifier>, where the identifier is the port name or port number. `+
		`With --docker-run, use <local port>:<container port> or <local port>:<container port>:<svcPortIdentifier>.`,
	)

	flags.StringVar(&ii.serviceName, "service", "", "Name of service to intercept. If not provided, we will try to auto-detect one")

	flags.BoolVarP(&ii.localOnly, "local-only", "l", false, ``+
		`Declare a local-only intercept for the purpose of getting direct outbound access to the intercept's namespace`)

	flags.BoolVarP(&ii.previewEnabled, "preview-url", "u", cliutil.HasLoggedIn(ctx), ``+
		`Generate an edgestack.me preview domain for this intercept. `+
		`(default "true" if you are logged in with 'telepresence login', default "false" otherwise)`,
	)
	addPreviewFlags("preview-url-", flags, &ii.previewSpec)

	flags.StringVarP(&ii.envFile, "env-file", "e", "", ``+
		`Also emit the remote environment to an env file in Docker Compose format. `+
		`See https://docs.docker.com/compose/env-file/ for more information on the limitations of this format.`)

	flags.StringVarP(&ii.envJSON, "env-json", "j", "", `Also emit the remote environment to a file as a JSON blob.`)

	flags.StringVarP(&ii.mount, "mount", "", "true", ``+
		`The absolute path for the root directory where volumes will be mounted, $TELEPRESENCE_ROOT. Use "true" to `+
		`have Telepresence pick a random mount point (default). Use "false" to disable filesystem mounting entirely.`)

	flags.StringSliceVar(&ii.toPod, "to-pod", []string{}, ``+
		`An additional port to forward from the intercepted pod, will be made available at localhost:PORT `+
		`Use this to, for example, access proxy/helper sidecars in the intercepted pod.`)

	flags.BoolVarP(&ii.dockerRun, "docker-run", "", false, ``+
		`Run a Docker container with intercepted environment, volume mount, by passing arguments after -- to 'docker run', `+
		`e.g. '--docker-run -- -it --rm ubuntu:20.04 /bin/bash'`)

	flags.StringVarP(&ii.dockerMount, "docker-mount", "", "", ``+
		`The volume mount point in docker. Defaults to same as "--mount"`)

	flags.StringVarP(&ii.namespace, "namespace", "n", "", "If present, the namespace scope for this CLI request")

	ii.extState, ii.extErr = extensions.LoadExtensions(ctx, flags)

	return cmd
}

func leaveCommand() *cobra.Command {
	return &cobra.Command{
		Use:  "leave [flags] <intercept_name>",
		Args: cobra.ExactArgs(1),

		Short: "Remove existing intercept",
		RunE:  removeIntercept,
	}
}

func (ii *interceptInfo) intercept(cmd *cobra.Command, args []string) error {
	if ii.extErr != nil {
		return ii.extErr
	}

	if ii.localOnly {
		// Sanity check for local-only intercept
		if ii.agentName != "" {
			return errors.New("a local-only intercept cannot have a workload")
		}
		if ii.serviceName != "" {
			return errors.New("a local-only intercept cannot have a service")
		}
		if cmd.Flag("port").Changed {
			return errors.New("a local-only intercept cannot have a port")
		}
		if cmd.Flag("mount").Changed {
			return errors.New("a local-only intercept cannot have mounts")
		}
		if cmd.Flag("preview-url").Changed && ii.previewEnabled {
			return errors.New("a local-only intercept cannot be previewed")
		}
	}

	extRequiresLogin, err := ii.extState.RequiresAPIKeyOrLicense()
	if err != nil {
		return err
	}

	ii.name = args[0]
	args = args[1:]
	if ii.agentName == "" && !ii.localOnly {
		ii.agentName = ii.name
		if ii.namespace != "" {
			ii.name += "-" + ii.namespace
		}
	}

	// Checks if login is necessary and then takes the necessary actions
	// depending if the cluster can connect to Ambassador Cloud
	loginIfNeeded := func(cs *connectorState) error {
		if ii.previewEnabled || extRequiresLogin {
			// We default to assuming they can connect to Ambassador Cloud
			// unless the cluster tells us they can't
			canConnect := true
			if resp, err := cs.managerClient.CanConnectAmbassadorCloud(cmd.Context(), &empty.Empty{}); err == nil {
				// We got a response from the manager; trust that response.
				canConnect = resp.CanConnect
			}
			if canConnect {
				if _, err := cliutil.EnsureLoggedIn(cmd.Context()); err != nil {
					return err
				}
			}
		}
		return nil
	}

	ii.cmd = cmd

	if len(args) == 0 && !ii.dockerRun {
		// start and retain the intercept
		return ii.withConnector(true, func(cs *connectorState) (err error) {
			err = loginIfNeeded(cs)
			if err != nil {
				return err
			}
			is := ii.newInterceptState(cs)
			return client.WithEnsuredState(is, true, func() error { return nil })
		})
	}

	// start intercept, run command, then stop the intercept
	if ii.dockerRun {
		if err = validateDockerArgs(args); err != nil {
			return err
		}
	}

	return ii.withConnector(false, func(cs *connectorState) error {
		err = loginIfNeeded(cs)
		if err != nil {
			return err
		}
		is := ii.newInterceptState(cs)
		return client.WithEnsuredState(is, false, func() error {
			if ii.dockerRun {
				return is.runInDocker(cmd, args)
			}
			return start(cmd.Context(), args[0], args[1:], true, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), envPairs(is.env)...)
		})
	})
}

// removeIntercept tells the daemon to deactivate and remove an existent intercept
func removeIntercept(cmd *cobra.Command, args []string) error {
	return withStartedConnector(cmd, func(cs *connectorState) error {
		is := &interceptInfo{name: strings.TrimSpace(args[0])}
		return is.newInterceptState(cs).DeactivateState()
	})
}

func (ii *interceptInfo) newInterceptState(cs *connectorState) *interceptState {
	return &interceptState{interceptInfo: ii, cs: cs, Scout: client.NewScout(cs.cmd.Context(), "cli")}
}

func interceptMessage(r *connector.InterceptResult) string {
	msg := ""
	switch r.Error {
	case connector.InterceptError_UNSPECIFIED:
	case connector.InterceptError_NO_PREVIEW_HOST:
		msg = `Your cluster is not configured for Preview URLs.
(Could not find a Host resource that enables Path-type Preview URLs.)
Please specify one or more header matches using --http-match.`
	case connector.InterceptError_NO_CONNECTION:
		msg = errConnectorIsNotRunning.Error()
	case connector.InterceptError_NO_TRAFFIC_MANAGER:
		msg = "Intercept unavailable: no traffic manager"
	case connector.InterceptError_TRAFFIC_MANAGER_CONNECTING:
		msg = "Connecting to traffic manager..."
	case connector.InterceptError_ALREADY_EXISTS:
		msg = fmt.Sprintf("Intercept with name %q already exists", r.ErrorText)
	case connector.InterceptError_LOCAL_TARGET_IN_USE:
		spec := r.InterceptInfo.Spec
		msg = fmt.Sprintf("Port %s:%d is already in use by intercept %s",
			spec.TargetHost, spec.TargetPort, r.ErrorText)
	case connector.InterceptError_NO_ACCEPTABLE_WORKLOAD:
		msg = fmt.Sprintf("No interceptable deployment or replicaset matching %s found", r.ErrorText)
	case connector.InterceptError_TRAFFIC_MANAGER_ERROR:
		msg = r.ErrorText
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
	case connector.InterceptError_FAILED_TO_REMOVE:
		msg = fmt.Sprintf("Error while removing intercept: %v", r.ErrorText)
	case connector.InterceptError_NOT_FOUND:
		msg = fmt.Sprintf("Intercept named %q not found", r.ErrorText)
	case connector.InterceptError_MOUNT_POINT_BUSY:
		msg = fmt.Sprintf("Mount point already in use by intercept %q", r.ErrorText)
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

func (is *interceptState) createRequest() (*connector.CreateInterceptRequest, error) {
	spec := &manager.InterceptSpec{
		Name:      is.name,
		Namespace: is.namespace,
	}
	ir := &connector.CreateInterceptRequest{Spec: spec}

	if is.agentName == "" {
		// local-only
		return ir, nil
	}

	if is.serviceName != "" {
		spec.ServiceName = is.serviceName
	}

	spec.Agent = is.agentName
	spec.TargetHost = "127.0.0.1"

	// Parse port into spec based on how it's formatted
	portMapping := strings.Split(is.port, ":")
	portError := func() error {
		if is.dockerRun {
			return errors.New("ports must be of the format --ports <local-port>:<container-port>[:<svcPortIdentifier>]")
		}
		return errors.New("ports must be of the format --ports <local-port>[:<svcPortIdentifier>]")
	}

	parsePort := func(portStr string) (uint16, error) {
		port, err := strconv.ParseUint(portStr, 10, 16)
		if err != nil {
			return 0, fmt.Errorf("port numbers must be a valid, positive int, you gave: %q", is.port)
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
		if port, err = parsePort(portMapping[1]); err == nil && is.dockerRun {
			is.dockerPort = port
		} else {
			spec.ServicePortIdentifier = portMapping[1]
		}
	case 3:
		if !is.dockerRun {
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

	if is.dockerRun && is.dockerPort == 0 {
		is.dockerPort = is.localPort
	}

	doMount := false
	err = checkMountCapability(is.cmd.Context())
	if err == nil {
		mountPoint := ""
		doMount, err = strconv.ParseBool(is.mount)
		if err != nil {
			mountPoint = is.mount
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
	} else if is.cmd.Flag("mount").Changed {
		var boolErr error
		doMount, boolErr = strconv.ParseBool(is.mount)
		if boolErr != nil || doMount {
			// not --mount=false, so refuse.
			return nil, fmt.Errorf("remote volume mounts are disabled: %w", err)
		}
	}

	for _, toPod := range is.toPod {
		port, err := parsePort(toPod)
		if err != nil {
			return nil, fmt.Errorf("Unable to parse port %s: %w", toPod, err)
		}
		spec.ExtraPorts = append(spec.ExtraPorts, int32(port))
	}

	if is.dockerMount != "" {
		if !is.dockerRun {
			return nil, errors.New("--docker-mount must be used together with --docker-run")
		}
		if !doMount {
			return nil, errors.New("--docker-mount cannot be used with --mount=false")
		}
	}

	spec.Mechanism, err = is.extState.Mechanism()
	if err != nil {
		return nil, err
	}
	spec.MechanismArgs, err = is.extState.MechanismArgs()
	if err != nil {
		return nil, err
	}

	var env client.Env
	env, err = client.LoadEnv(is.cmd.Context())
	if err != nil {
		return nil, err
	}
	ir.AgentImage, err = is.extState.AgentImage(is.cmd.Context(), env)
	if err != nil {
		return nil, err
	}
	return ir, nil
}

func (is *interceptState) EnsureState() (acquired bool, err error) {
	// Fill defaults
	if is.previewEnabled && is.previewSpec.Ingress == nil {
		ingress, err := is.cs.selectIngress(is.cmd.Context(), is.cmd.InOrStdin(), is.cmd.OutOrStdout())
		if err != nil {
			return false, err
		}
		is.previewSpec.Ingress = ingress
	}

	ir, err := is.createRequest()
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
	r, err := is.cs.connectorClient.CreateIntercept(is.cmd.Context(), ir)
	if err != nil {
		return false, fmt.Errorf("connector.CreateIntercept: %w", err)
	}

	switch r.Error {
	case connector.InterceptError_UNSPECIFIED:
		if is.agentName == "" {
			// local-only
			return true, nil
		}
		fmt.Fprintf(is.cmd.OutOrStdout(), "Using %s %s\n", r.WorkloadKind, is.agentName)
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
		is.Scout.SetMetadatum("service_name", is.agentName)
		is.Scout.SetMetadatum("cluster_id", is.cs.info.ClusterId)

		mechanism, _ := is.extState.Mechanism()
		mechanismArgs, _ := is.extState.MechanismArgs()
		is.Scout.SetMetadatum("intercept_mechanism", mechanism)
		is.Scout.SetMetadatum("intercept_mechanism_numargs", len(mechanismArgs))

		if is.previewEnabled {
			intercept, err = is.cs.managerClient.UpdateIntercept(is.cmd.Context(), &manager.UpdateInterceptRequest{
				Session: is.cs.info.SessionInfo,
				Name:    is.name,
				PreviewDomainAction: &manager.UpdateInterceptRequest_AddPreviewDomain{
					AddPreviewDomain: &is.previewSpec,
				},
			})
			if err != nil {
				_ = is.Scout.Report(is.cmd.Context(), "preview_domain_create_fail", client.ScoutMeta{Key: "error", Value: err.Error()})
				err = fmt.Errorf("creating preview domain: %w", err)
				return true, err
			}
			is.Scout.SetMetadatum("preview_url", intercept.PreviewDomain)
		} else {
			intercept = r.InterceptInfo
		}
		is.Scout.SetMetadatum("intercept_id", intercept.Id)

		is.env = r.Environment
		if is.envFile != "" {
			if err = is.writeEnvFile(); err != nil {
				return true, err
			}
		}
		if is.envJSON != "" {
			if err = is.writeEnvJSON(); err != nil {
				return true, err
			}
		}

		var volumeMountProblem error
		doMount, err := strconv.ParseBool(is.mount)
		if doMount || err != nil {
			volumeMountProblem = checkMountCapability(is.cmd.Context())
		}
		fmt.Fprintln(is.cmd.OutOrStdout(), DescribeIntercept(intercept, volumeMountProblem, false))
		_ = is.Scout.Report(is.cmd.Context(), "intercept_success")
		return true, nil
	case connector.InterceptError_ALREADY_EXISTS:
		fmt.Fprintln(is.cmd.OutOrStdout(), interceptMessage(r))
		return false, nil
	case connector.InterceptError_NO_CONNECTION:
		return false, errConnectorIsNotRunning
	default:
		if r.GetInterceptInfo().GetDisposition() == manager.InterceptDispositionType_BAD_ARGS {
			_ = is.DeactivateState()
			_ = is.cmd.FlagErrorFunc()(is.cmd, errors.New(r.InterceptInfo.Message))
			panic("not reached; FlagErrorFunc should call os.Exit()")
		}
		return false, errors.New(interceptMessage(r))
	}
}

func (is *interceptState) DeactivateState() error {
	name := strings.TrimSpace(is.name)
	var r *connector.InterceptResult
	var err error
	r, err = is.cs.connectorClient.RemoveIntercept(context.Background(), &manager.RemoveInterceptRequest2{Name: name})
	if err != nil {
		return err
	}
	if r.Error != connector.InterceptError_UNSPECIFIED {
		return errors.New(interceptMessage(r))
	}
	return nil
}

func validateDockerArgs(args []string) error {
	for _, arg := range args {
		if arg == "-d" || arg == "--detach" {
			return errors.New("running docker container in background using -d or --detach is not supported")
		}
	}
	return nil
}

func (is *interceptState) runInDocker(cmd *cobra.Command, args []string) error {
	if is.envFile == "" {
		file, err := ioutil.TempFile("", "tel-*.env")
		if err != nil {
			return fmt.Errorf("failed to create temporary environment file. %w", err)
		}
		defer os.Remove(file.Name())

		if err = is.writeEnvToFileAndClose(file); err != nil {
			return err
		}
		is.envFile = file.Name()
	}

	ourArgs := []string{
		"run",
		"--dns-search", "tel2-search",
		"--env-file", is.envFile,
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
		ourArgs = append(ourArgs, "--name", fmt.Sprintf("intercept-%s-%d", is.name, is.localPort))
	}

	if is.dockerPort != 0 {
		ourArgs = append(ourArgs, "-p", fmt.Sprintf("%d:%d", is.localPort, is.dockerPort))
	}

	dockerMount := ""
	if is.mountPoint != "" { // do we have a mount point at all?
		if dockerMount = is.dockerMount; dockerMount == "" {
			dockerMount = is.mountPoint
		}
	}
	if dockerMount != "" {
		ourArgs = append(ourArgs, "-v", fmt.Sprintf("%s:%s", is.mountPoint, dockerMount))
	}
	return start(cmd.Context(), "docker", append(ourArgs, args...), true, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
}

func (is *interceptState) writeEnvFile() error {
	file, err := os.Create(is.envFile)
	if err != nil {
		return fmt.Errorf("failed to create environment file %q: %w", is.envFile, err)
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
	return ioutil.WriteFile(is.envJSON, data, 0644)
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

func (cs *connectorState) selectIngress(ctx context.Context, in io.Reader, out io.Writer) (*manager.IngressInfo, error) {
	infos, err := cache.LoadIngressesFromUserCache(ctx)
	if err != nil {
		return nil, err
	}
	key := cs.info.ClusterServer + "/" + cs.info.ClusterContext
	selectOrConfirm := "Confirm"
	cachedIngressInfo := infos[key]
	if cachedIngressInfo == nil {
		iis := cs.info.IngressInfos
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
