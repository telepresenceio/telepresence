package intercept

import (
	"context"
	"fmt"
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
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
)

type Args struct {
	Name        string // Args[0] || `${Args[0]}-${--namespace}` // which depends on a combinationof --workload and --namespace
	AgentName   string // --workload || Args[0] // only valid if !localOnly
	Namespace   string // --namespace
	Port        string // --port // only valid if !localOnly
	ServiceName string // --service // only valid if !localOnly
	LocalOnly   bool   // --local-only

	EnvFile  string   // --env-file
	EnvJSON  string   // --env-json
	Mount    string   // --mount // "true", "false", or desired mount point // only valid if !localOnly
	MountSet bool     // whether --mount was passed
	ToPod    []string // --to-pod

	DockerRun   bool     // --docker-run
	DockerMount string   // --docker-mount // where to mount in a docker container. Defaults to mount unless mount is "true" or "false".
	Cmdline     []string // Args[1:]
}

func Command(ctx context.Context) *cobra.Command {
	a := &Args{}
	cmd := &cobra.Command{
		Use:   "intercept [flags] <intercept_base_name> [-- <command with arguments...>]",
		Args:  cobra.MinimumNArgs(1),
		Short: "Intercept a service",
		Annotations: map[string]string{
			ann.RootDaemon: ann.Required,
			ann.Session:    ann.Required,
		},
		SilenceUsage:      true,
		SilenceErrors:     true,
		RunE:              a.Run,
		ValidArgsFunction: a.ValidArgs,
		PreRunE:           util.UpdateCheckIfDue,
		PostRunE:          util.RaiseCloudMessage,
	}
	a.AddFlags(ctx, cmd.Flags())
	if err := cmd.RegisterFlagCompletionFunc("namespace", a.AutocompleteNamespace); err != nil {
		log.Fatal(err)
	}
	return cmd
}

func (a *Args) AddFlags(ctx context.Context, flags *pflag.FlagSet) {
	flags.StringVarP(&a.AgentName, "workload", "w", "", "Name of workload (Deployment, ReplicaSet) to intercept, if different from <name>")
	flags.StringVarP(&a.Port, "port", "p", strconv.Itoa(client.GetConfig(ctx).Intercept.DefaultPort), ``+
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
}

func (a *Args) Validate(cmd *cobra.Command, positional []string) error {
	if len(positional) > 1 && cmd.Flags().ArgsLenAtDash() != 1 {
		return fmt.Errorf("commands to be run with intercept must come after options")
	}
	a.Name = positional[0]
	a.Cmdline = positional[1:]
	switch a.LocalOnly { // a switch instead of an if/else to get gocritic to not suggest "else if"
	case true:
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
	case false:
		// Actually intercepting something
		if a.AgentName == "" {
			a.AgentName = a.Name
			if a.Namespace != "" {
				a.Name += "-" + a.Namespace
			}
		}
	}
	a.MountSet = cmd.Flag("mount").Changed
	if a.DockerRun {
		if err := a.ValidateDockerArgs(); err != nil {
			return err
		}
	}
	return util.InitCommand(cmd)
}

func (a *Args) Run(cmd *cobra.Command, positional []string) error {
	if err := a.Validate(cmd, positional); err != nil {
		return err
	}
	return NewState(cmd, a).Intercept()
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
	if err := util.InitCommand(cmd); err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	ctx := cmd.Context()
	var (
		namespaceArgsIdx int
		namespaceArg     string
		namespace        = "default"
	)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "--namespace") || strings.HasPrefix(arg, "-n") {
			namespaceArgsIdx = i
			namespaceArg = args[i]
			break
		}
	}
	namespaceArgParts := strings.Split(namespaceArg, "=")
	if len(namespaceArgParts) == 2 {
		namespace = namespaceArgParts[1]
	} else if namespaceArgsIdx+1 < len(args) {
		namespace = args[namespaceArgsIdx+1]
	}

	req := connector.ListRequest{
		Filter:    connector.ListRequest_INTERCEPTABLE,
		Namespace: namespace,
	}
	cs := util.GetUserDaemon(ctx)
	r, err := cs.List(ctx, &req)
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
