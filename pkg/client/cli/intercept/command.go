package intercept

import (
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/flags"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

type Command struct {
	Name           string // Command[0] || `${Command[0]}-${--namespace}` // which depends on a combinationof --workload and --namespace
	AgentName      string // --workload || Command[0] // only valid if !localOnly
	Port           string // --port // only valid if !localOnly
	ServiceName    string // --service // only valid if !localOnly
	Address        string // --address // only valid if !localOnly
	LocalOnly      bool   // --local-only
	LocalMountPort uint16 // --local-mount-port

	Replace bool // whether --replace was passed

	EnvFile  string   // --env-file
	EnvJSON  string   // --env-json
	Mount    string   // --mount // "true", "false", or desired mount point // only valid if !localOnly
	MountSet bool     // whether --mount was passed
	ToPod    []string // --to-pod

	DockerRun          bool     // --docker-run
	DockerBuild        string   // --docker-build DIR | URL
	DockerBuildOptions []string // --docker-build-opt key=value, // Optional flag to docker build can be repeated (but not comma separated)
	DockerDebug        string   // --docker-debug DIR | URL
	DockerMount        string   // --docker-mount // where to mount in a docker container. Defaults to mount unless mount is "true" or "false".
	Cmdline            []string // Command[1:]

	Mechanism      string // --mechanism tcp
	MechanismArgs  []string
	ExtendedInfo   []byte
	DetailedOutput bool
}

func (a *Command) AddFlags(cmd *cobra.Command) {
	flagSet := cmd.Flags()
	flagSet.StringVarP(&a.AgentName, "workload", "w", "", "Name of workload (Deployment, ReplicaSet) to intercept, if different from <name>")
	flagSet.StringVarP(&a.Port, "port", "p", "", ``+
		`Local port to forward to. If intercepting a service with multiple ports, `+
		`use <local port>:<svcPortIdentifier>, where the identifier is the port name or port number. `+
		`With --docker-run and a daemon that doesn't run in docker', use <local port>:<container port> or `+
		`<local port>:<container port>:<svcPortIdentifier>.`,
	)

	flagSet.StringVar(&a.Address, "address", "127.0.0.1", ``+
		`Local address to forward to, Only accepts IP address as a value. `+
		`e.g. '--address 10.0.0.2'`,
	)

	flagSet.StringVar(&a.ServiceName, "service", "", "Name of service to intercept. If not provided, we will try to auto-detect one")

	flagSet.BoolVarP(&a.LocalOnly, "local-only", "l", false, ``+
		`Declare a local-only intercept for the purpose of getting direct outbound access to the intercept's namespace`)

	flagSet.StringVarP(&a.EnvFile, "env-file", "e", "", ``+
		`Also emit the remote environment to an env file in Docker Compose format. `+
		`See https://docs.docker.com/compose/env-file/ for more information on the limitations of this format.`)

	flagSet.StringVarP(&a.EnvJSON, "env-json", "j", "", `Also emit the remote environment to a file as a JSON blob.`)

	flagSet.StringVar(&a.Mount, "mount", "true", ``+
		`The absolute path for the root directory where volumes will be mounted, $TELEPRESENCE_ROOT. Use "true" to `+
		`have Telepresence pick a random mount point (default). Use "false" to disable filesystem mounting entirely.`)

	flagSet.StringSliceVar(&a.ToPod, "to-pod", []string{}, ``+
		`An additional port to forward from the intercepted pod, will be made available at localhost:PORT `+
		`Use this to, for example, access proxy/helper sidecars in the intercepted pod. The default protocol is TCP. `+
		`Use <port>/UDP for UDP ports`)

	flagSet.BoolVar(&a.DockerRun, "docker-run", false, ``+
		`Run a Docker container with intercepted environment, volume mount, by passing arguments after -- to 'docker run', `+
		`e.g. '--docker-run -- -it --rm ubuntu:20.04 /bin/bash'`)

	flagSet.StringVar(&a.DockerBuild, "docker-build", "", ``+
		`Build a Docker container from the given docker-context (path or URL), and run it with intercepted environment and volume mounts, `+
		`by passing arguments after -- to 'docker run', e.g. '--docker-build /path/to/docker/context -- -it IMAGE /bin/bash'`)

	flagSet.StringVar(&a.DockerDebug, "docker-debug", "", ``+
		`Like --docker-build, but allows a debugger to run inside the container with relaxed security`)

	flagSet.StringArrayVar(&a.DockerBuildOptions, "docker-build-opt", nil,
		`Option to docker-build in the form key=value, e.g. --docker-build-opt tag=mytag. Can be repeated`)

	flagSet.StringVar(&a.DockerMount, "docker-mount", "", ``+
		`The volume mount point in docker. Defaults to same as "--mount"`)

	flagSet.StringP("namespace", "n", "", "If present, the namespace scope for this CLI request")

	flagSet.StringVar(&a.Mechanism, "mechanism", "tcp", "Which extension `mechanism` to use")

	flagSet.BoolVar(&a.DetailedOutput, "detailed-output", false,
		`Provide very detailed info about the intercept when used together with --output=json or --output=yaml'`)

	flagSet.Uint16Var(&a.LocalMountPort, "local-mount-port", 0,
		`Do not mount remote directories. Instead, expose this port on localhost to an external mounter`)

	flagSet.BoolVarP(&a.Replace, "replace", "", false,
		`Indicates if the traffic-agent should replace application containers in workload pods. `+
			`The default behavior is for the agent sidecar to be installed alongside existing containers.`)

	// Hide these flags. They are still functional but deprecated. Using them will yield a deprecation message.
	flagSet.Lookup("local-only").Hidden = true
	flagSet.Lookup("namespace").Hidden = true
}

func (a *Command) Validate(cmd *cobra.Command, positional []string) error {
	flags.DeprecationIfChanged(cmd, "local-only", "use telepresence connect to set the namespace")
	flags.DeprecationIfChanged(cmd, "namespace", "use telepresence connect to set the namespace")
	if len(positional) > 1 && cmd.Flags().ArgsLenAtDash() != 1 {
		return errcat.User.New("commands to be run with intercept must come after options")
	}
	a.Name = positional[0]
	a.Cmdline = positional[1:]
	if a.LocalOnly {
		// Not actually intercepting anything -- check that the flags make sense for that
		if a.AgentName != "" {
			return errcat.User.New("a local-only intercept cannot have a workload")
		}
		if a.ServiceName != "" {
			return errcat.User.New("a local-only intercept cannot have a service")
		}
		if cmd.Flag("port").Changed {
			return errcat.User.New("a local-only intercept cannot have a port")
		}
		if cmd.Flag("mount").Changed {
			if doMount, _ := a.GetMountPoint(); doMount {
				return errcat.User.New("a local-only intercept cannot have mounts")
			}
		}
		return nil
	}

	if a.LocalMountPort > 0 && client.GetConfig(cmd.Context()).Intercept().UseFtp {
		return errcat.User.New("only SFTP can be used with --local-mount-port. Client is configured to perform remote mounts using FTP")
	}

	// Actually intercepting something
	if a.AgentName == "" {
		a.AgentName = a.Name
	}
	if a.Port == "" {
		a.Port = strconv.Itoa(client.GetConfig(cmd.Context()).Intercept().DefaultPort)
	}
	a.MountSet = cmd.Flag("mount").Changed
	drCount := 0
	if a.DockerRun {
		drCount++
	}
	if a.DockerBuild != "" {
		drCount++
	}
	if a.DockerDebug != "" {
		drCount++
	}
	if drCount > 1 {
		return errcat.User.New("only one of --docker-run, --docker-build, or --docker-debug can be used")
	}
	a.DockerRun = drCount == 1
	if a.DockerRun {
		if err := a.ValidateDockerArgs(); err != nil {
			return err
		}
	}
	return nil
}

func (a *Command) Run(cmd *cobra.Command, positional []string) error {
	if err := a.Validate(cmd, positional); err != nil {
		return err
	}
	if err := connect.InitCommand(cmd); err != nil {
		return err
	}
	return NewState(cmd, a).Run(cmd.Context())
}

func (a *Command) ValidateDockerArgs() error {
	for _, arg := range a.Cmdline {
		if arg == "-d" || arg == "--detach" {
			return errcat.User.New("running docker container in background using -d or --detach is not supported")
		}
	}
	return nil
}

func (a *Command) ValidArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		// Not completing the name of the workload
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	if err := connect.InitCommand(cmd); err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	req := connector.ListRequest{
		Filter: connector.ListRequest_INTERCEPTABLE,
	}
	nf := cmd.Flag("namespace")
	if nf.Changed {
		req.Namespace = nf.Value.String()
	}
	ctx := cmd.Context()

	// Trace level is used here, because we generally don't want to log expansion attempts
	// in the cli.log
	dlog.Tracef(ctx, "ns = %s, toComplete = %s, args = %v", req.Namespace, toComplete, args)
	r, err := daemon.GetUserClient(ctx).List(ctx, &req)
	if err != nil {
		dlog.Debugf(ctx, "unable to get list of interceptable workloads: %v", err)
		return nil, cobra.ShellCompDirectiveError
	}

	list := make([]string, 0)
	for _, w := range r.Workloads {
		// only suggest strings that start with the string were autocompleting
		if strings.HasPrefix(w.Name, toComplete) {
			list = append(list, w.Name)
		}
	}

	// TODO(raphaelreyna): This list can be quite large (in the double digits of MB).
	// There probably exists a number that would be a good cutoff limit.

	return list, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
}

// GetMountPoint returns a boolean indicating if mounts are enabled or not, and path
// indicating a mount point.
func (a *Command) GetMountPoint() (bool, string) {
	if !a.MountSet {
		// Default is that mount is enabled and the path is unspecified
		return true, ""
	}
	if doMount, err := strconv.ParseBool(a.Mount); err == nil {
		// Boolean flag, path unspecified
		return doMount, ""
	}
	if len(a.Mount) == 0 {
		// Let explicit --mount= have the same meaning as --mount=false
		return false, ""
	}
	return true, a.Mount
}
