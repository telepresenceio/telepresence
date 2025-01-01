package intercept

import (
	"errors"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/env"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ingest"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/mount"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

type Command struct {
	EnvFlags      env.Flags
	DockerFlags   docker.Flags
	MountFlags    mount.Flags
	Name          string   // Command[0] || `${Command[0]}-${--namespace}` // which depends on a combinationof --workload and --namespace
	AgentName     string   // --workload || Command[0] // only valid if !localOnly
	Ports         []string // --port
	ServiceName   string   // --service
	ContainerName string   // --container
	Address       string   // --address

	Replace bool // whether --replace was passed

	ToPod []string // --to-pod

	Cmdline []string // Command[1:]

	Mechanism       string // --mechanism tcp
	MechanismArgs   []string
	ExtendedInfo    []byte
	WaitMessage     string // Message printed when a containerized intercept handler is started and waiting for an interrupt
	FormattedOutput bool
	DetailedOutput  bool
	Silent          bool
}

func (c *Command) AddFlags(cmd *cobra.Command) {
	flagSet := cmd.Flags()
	flagSet.StringVarP(&c.AgentName, "workload", "w", "", "Name of workload (Deployment, ReplicaSet, StatefulSet, Rollout) to intercept, if different from <name>")
	flagSet.StringSliceVarP(&c.Ports, "port", "p", nil, ``+
		`Local ports to forward to. Use <local port>:<identifier> to uniquely identify service ports, where the <identifier> is the port name or number. `+
		`With --docker-run and a daemon that doesn't run in docker', use <local port>:<container port> or `+
		`<local port>:<container port>:<identifier>.`,
	)

	flagSet.StringVar(&c.Address, "address", "127.0.0.1", ``+
		`Local address to forward to, Only accepts IP address as a value. `+
		`e.g. '--address 10.0.0.2'`,
	)

	flagSet.StringVar(&c.ServiceName, "service", "", "Name of service to intercept. If not provided, we will try to auto-detect one")

	flagSet.StringVar(&c.ContainerName, "container", "",
		"Name of container that provides the environment and mounts for the intercept. Defaults to the container matching the targetPort")

	flagSet.StringSliceVar(&c.ToPod, "to-pod", []string{}, ``+
		`An additional port to forward to the intercepted pod, will available for connections to localhost:PORT. `+
		`Use this to, for example, access proxy/helper sidecars in the intercepted pod. The default protocol is TCP. `+
		`Use <port>/UDP for UDP ports`)

	c.EnvFlags.AddFlags(flagSet)
	c.MountFlags.AddFlags(flagSet, false)
	c.DockerFlags.AddFlags(flagSet, "intercepted")

	flagSet.StringVar(&c.Mechanism, "mechanism", "tcp", "Which extension `mechanism` to use")

	flagSet.StringVar(&c.WaitMessage, "wait-message", "", "Message to print when intercept handler has started")

	flagSet.BoolVar(&c.DetailedOutput, "detailed-output", false,
		`Provide very detailed info about the intercept when used together with --output=json or --output=yaml'`)

	flagSet.BoolVarP(&c.Replace, "replace", "", false,
		`Indicates if the traffic-agent should replace application containers in workload pods. `+
			`The default behavior is for the agent sidecar to be installed alongside existing containers.`)

	_ = cmd.RegisterFlagCompletionFunc("container", ingest.AutocompleteContainer)
	_ = cmd.RegisterFlagCompletionFunc("service", autocompleteService)
}

func (c *Command) Validate(cmd *cobra.Command, positional []string) error {
	if len(positional) > 1 && cmd.Flags().ArgsLenAtDash() != 1 {
		return errcat.User.New("commands to be run with intercept must come after options")
	}
	c.Name = positional[0]
	c.Cmdline = positional[1:]
	c.FormattedOutput = output.WantsFormatted(cmd)

	// Actually intercepting something
	if c.AgentName == "" {
		c.AgentName = c.Name
	}
	if len(c.Ports) == 0 {
		// Port defaults to the targeted container port unless a default is explicitly set in the client config.
		if dp := client.GetConfig(cmd.Context()).Intercept().DefaultPort; dp != 0 {
			c.Ports = []string{strconv.Itoa(dp)}
		}
	}
	if err := c.MountFlags.Validate(cmd); err != nil {
		return err
	}
	if c.DockerFlags.Mount != "" && !c.MountFlags.Enabled {
		return errors.New("--docker-mount cannot be used with --mount=false")
	}
	return c.DockerFlags.Validate(c.Cmdline)
}

func (c *Command) Run(cmd *cobra.Command, positional []string) error {
	if err := c.Validate(cmd, positional); err != nil {
		return err
	}
	if err := connect.InitCommand(cmd); err != nil {
		return err
	}
	ctx := dos.WithStdio(cmd.Context(), cmd)
	_, err := NewState(c, c.MountFlags.ValidateConnected(ctx)).Run(ctx)
	return err
}

func autocompleteService(cmd *cobra.Command, args []string, toComplete string) (serviceNames []string, directive cobra.ShellCompDirective) {
	ctx, s, err := connect.GetOptionalSession(cmd)
	if s == nil || err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	if len(args) == 0 {
		ctx, kc, err := daemon.GetCommandKubeConfig(cmd)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		ki, err := kubernetes.NewForConfig(kc.RestConfig)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		svcs, err := k8sapi.Services(k8sapi.WithK8sInterface(ctx, ki), kc.Namespace, nil)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		for _, svc := range svcs {
			n := svc.GetName()
			if toComplete == "" || strings.HasPrefix(n, toComplete) {
				serviceNames = append(serviceNames, n)
			}
		}
		return serviceNames, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
	}
	sc, err := s.GetAgentConfig(ctx, args[0])
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	cn := cmd.Flag("container").Value.String()
	for _, c := range sc.Containers {
		if cn != "" && cn != c.Name {
			continue
		}
		for _, ic := range c.Intercepts {
			n := ic.ServiceName
			if toComplete == "" || strings.HasPrefix(n, toComplete) {
				serviceNames = append(serviceNames, n)
			}
		}
	}
	sort.Strings(serviceNames)
	return slices.Compact(serviceNames), cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
}

func ValidArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	// Trace level is used here, because we generally don't want to log expansion attempts
	// in the cli.log
	dlog.Tracef(cmd.Context(), "toComplete = %s, args = %v", toComplete, args)

	if len(args) > 0 {
		if slices.Contains(os.Args, "--") {
			if cmd.Flag("docker-run").Changed {
				return docker.AutocompleteRun(cmd, args[1:], toComplete)
			}
			// Scan for command to execute
			return nil, cobra.ShellCompDirectiveDefault
		}
		// Not completing the name of the workload
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	if err := connect.InitCommand(cmd); err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	req := connector.ListRequest{
		Filter: connector.ListRequest_INTERCEPTABLE,
	}
	ctx := cmd.Context()

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
