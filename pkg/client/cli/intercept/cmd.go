package intercept

import (
	"context"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/util"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

type Args struct {
	Name        string // Args[0] || `${Args[0]}-${--namespace}` // which depends on a combinationof --workload and --namespace
	AgentName   string // --workload || Args[0] // only valid if !localOnly
	Namespace   string // --namespace
	Port        string // --port // only valid if !localOnly
	ServiceName string // --service // only valid if !localOnly
	LocalOnly   bool   // --local-only

	HttpHeader []string // --http-header

	EnvFile  string   // --env-file
	EnvJSON  string   // --env-json
	Mount    string   // --mount // "true", "false", or desired mount point // only valid if !localOnly
	MountSet bool     // whether --mount was passed
	ToPod    []string // --to-pod

	DockerRun   bool     // --docker-run
	DockerMount string   // --docker-mount // where to mount in a docker container. Defaults to mount unless mount is "true" or "false".
	Cmdline     []string // Args[1:]

	Mechanism     string // --mechanism tcp or http
	MechanismArgs []string
	ExtendedInfo  []byte
}

func Command() *cobra.Command {
	a := &Args{}
	cmd := &cobra.Command{
		Use:   "intercept [flags] <intercept_base_name> [-- <command with arguments...>]",
		Args:  cobra.MinimumNArgs(1),
		Short: "Intercept a service",
		Annotations: map[string]string{
			ann.RootDaemon:        ann.Required,
			ann.Session:           ann.Required,
			ann.UpdateCheckFormat: ann.Tel2,
		},
		SilenceUsage:      true,
		SilenceErrors:     true,
		RunE:              a.Run,
		ValidArgsFunction: a.ValidArgs,
	}
	a.AddFlags(cmd.Flags())
	if err := cmd.RegisterFlagCompletionFunc("namespace", a.AutocompleteNamespace); err != nil {
		log.Fatal(err)
	}
	return cmd
}

func (a *Args) AddFlags(flags *pflag.FlagSet) {
	flags.StringVarP(&a.AgentName, "workload", "w", "", "Name of workload (Deployment, ReplicaSet) to intercept, if different from <name>")
	flags.StringVarP(&a.Port, "port", "p", "", ``+
		`Local port to forward to. If intercepting a service with multiple ports, `+
		`use <local port>:<svcPortIdentifier>, where the identifier is the port name or port number. `+
		`With --docker-run, use <local port>:<container port> or <local port>:<container port>:<svcPortIdentifier>.`,
	)

	flags.StringVar(&a.ServiceName, "service", "", "Name of service to intercept. If not provided, we will try to auto-detect one")

	flags.BoolVarP(&a.LocalOnly, "local-only", "l", false, ``+
		`Declare a local-only intercept for the purpose of getting direct outbound access to the intercept's namespace`)

	flags.StringVarP(&a.EnvFile, "env-file", "e", "", ``+
		`Also emit the remote environment to an env file in Docker Compose format. `+
		`See https://docs.docker.com/compose/env-file/ for more information on the limitations of this format.`)

	flags.StringVarP(&a.EnvJSON, "env-json", "j", "", `Also emit the remote environment to a file as a JSON blob.`)

	flags.StringVarP(&a.Mount, "mount", "", "true", ``+
		`The absolute path for the root directory where volumes will be mounted, $TELEPRESENCE_ROOT. Use "true" to `+
		`have Telepresence pick a random mount point (default). Use "false" to disable filesystem mounting entirely.`)

	flags.StringSliceVar(&a.ToPod, "to-pod", []string{}, ``+
		`An additional port to forward from the intercepted pod, will be made available at localhost:PORT `+
		`Use this to, for example, access proxy/helper sidecars in the intercepted pod. The default protocol is TCP. `+
		`Use <port>/UDP for UDP ports`)

	flags.BoolVarP(&a.DockerRun, "docker-run", "", false, ``+
		`Run a Docker container with intercepted environment, volume mount, by passing arguments after -- to 'docker run', `+
		`e.g. '--docker-run -- -it --rm ubuntu:20.04 /bin/bash'`)

	flags.StringVarP(&a.DockerMount, "docker-mount", "", "", ``+
		`The volume mount point in docker. Defaults to same as "--mount"`)

	flags.StringVarP(&a.Namespace, "namespace", "n", "", "If present, the namespace scope for this CLI request")

	flags.StringVar(&a.Mechanism, "mechanism", "tcp", "Which extension `mechanism` to use (http and tcp supported)")

	flags.StringArrayVar(&a.HttpHeader, "http-header", []string{"auto"}, "Only intercept traffic that matches this \"HTTP2_HEADER=REGEXP\" specifier."+
		" Instead of a \"--http-header=HTTP2_HEADER=REGEXP\" pair, you may say \"--http-header=auto\", "+
		"which will automatically select a unique matcher for your intercept.")
}

func (a *Args) Validate(cmd *cobra.Command, positional []string) error {
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
			return errcat.User.New("a local-only intercept cannot have mounts")
		}
		return nil
	}
	// Actually intercepting something
	if a.AgentName == "" {
		a.AgentName = a.Name
		if a.Namespace != "" {
			a.Name += "-" + a.Namespace
		}
	}
	if a.Port == "" {
		a.Port = strconv.Itoa(client.GetConfig(cmd.Context()).Intercept.DefaultPort)
	}
	a.MountSet = cmd.Flag("mount").Changed
	if a.DockerRun {
		if err := a.ValidateDockerArgs(); err != nil {
			return err
		}
	}
	return nil
}

func (a *Args) Run(cmd *cobra.Command, positional []string) error {
	if err := a.Validate(cmd, positional); err != nil {
		return err
	}
	if err := util.InitCommand(cmd); err != nil {
		return err
	}
	return Run(cmd.Context(), NewState(cmd, a))
}

func (a *Args) AutocompleteNamespace(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if err := util.InitCommand(cmd); err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	ctx := cmd.Context()
	ud := util.GetUserDaemon(ctx)
	rs, err := ud.GetNamespaces(ctx, &connector.GetNamespacesRequest{
		ForClientAccess: true,
		Prefix:          toComplete,
	})
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	return rs.Namespaces, cobra.ShellCompDirectiveNoFileComp
}

func (a *Args) ValidateDockerArgs() error {
	for _, arg := range a.Cmdline {
		if arg == "-d" || arg == "--detach" {
			return errcat.User.New("running docker container in background using -d or --detach is not supported")
		}
	}
	return nil
}

func (a *Args) ValidArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		// Not completing the name of the workload
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	if err := util.InitCommand(cmd); err != nil {
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
	r, err := util.GetUserDaemon(ctx).List(ctx, &req)
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

func (a *Args) GetMountPoint(ctx context.Context) (string, bool, error) {
	mountPoint := ""
	doMount, err := strconv.ParseBool(a.Mount)
	if err != nil {
		mountPoint = a.Mount
		doMount = len(mountPoint) > 0
		err = nil
	}

	if doMount {
		var cwd string
		cwd, err = os.Getwd()
		if err != nil {
			return "", false, err
		}
		mountPoint, err = util.PrepareMount(cwd, mountPoint)
	}

	return mountPoint, doMount, err
}
