package ingest

import (
	"errors"
	"slices"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/env"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/flags"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/mount"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/progress"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

type Command struct {
	EnvFlags        env.Flags
	DockerFlags     docker.Flags
	MountFlags      mount.Flags
	WorkloadName    string // --workload || Command[0] // only valid if !localOnly
	ContainerName   string // --container
	Namespace       string // --namespace
	WaitMessage     string
	ToPod           []string // --to-pod
	Cmdline         []string
	FormattedOutput bool
	NodeAgent       bool // --node-agent
}

func (c *Command) AddFlags(cmd *cobra.Command) {
	flagSet := cmd.Flags()
	flagSet.StringVarP(&c.ContainerName, "container", "c", "", "Name of container that provides the environment and mounts for the ingest")
	flagSet.StringVarP(&c.Namespace, "namespace", "n", "", "Namespace containing the workload to ingest. Defaults to the connected namespace")

	flagSet.StringSliceVar(&c.ToPod, "to-pod", []string{}, ``+
		`An additional port to forward from the ingested pod, will be made available at localhost:PORT `+
		`Use this to, for example, access proxy/helper sidecars in the ingested pod. The default protocol is TCP. `+
		`Use <port>/UDP for UDP ports`)

	c.EnvFlags.AddFlags(flagSet)
	c.MountFlags.AddFlags(flagSet, true)
	c.DockerFlags.AddFlags(flagSet, "ingested")
	flagSet.StringVar(&c.WaitMessage, "wait-message", "", "Message to print when ingest handler has started")

	flagSet.BoolVar(&c.NodeAgent, "node-agent", false,
		"Serve this ingest with a node-hosted traffic-agent (a manager-created Job that enters the target pod's namespaces) instead of injecting a sidecar. "+
			"Requires the traffic-manager to have node-agent mode enabled.")

	_ = cmd.RegisterFlagCompletionFunc("container", AutocompleteContainer)
}

func (c *Command) Validate(cmd *cobra.Command, positional []string) error {
	if len(positional) > 1 && cmd.Flags().ArgsLenAtDash() != 1 {
		return errcat.User.New("commands to be run with ingest must come after options")
	}
	c.WorkloadName = positional[0]
	c.Cmdline = positional[1:]
	c.FormattedOutput = output.WantsFormatted(cmd)
	// --node-agent is resolved in Run, once a session is available (see
	// flags.NodeAgentDefault): Validate runs too early to reach the
	// session-merged client config.
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
	c.NodeAgent = flags.NodeAgentDefault(cmd, c.NodeAgent)
	defer progress.Stop(cmd.Context())
	ctx := dos.WithStdio(cmd.Context(), cmd)
	return NewState(c, c.MountFlags.ValidateConnected(ctx)).Run(ctx)
}

func AutocompleteContainer(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return nil, cobra.ShellCompDirectiveError
	}
	ctx, s, err := connect.GetOptionalSession(cmd)
	if s == nil || err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	sc, err := s.GetAgentConfig(ctx, args[0])
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	css := make([]string, 0, len(sc.Containers))
	var svcName string
	if sf := cmd.Flags().Lookup("service"); sf != nil && sf.Changed {
		// Only include containers matching this service
		svcName = sf.Value.String()
	}
	for _, c := range sc.Containers {
		if svcName == "" || slices.ContainsFunc(c.Intercepts, func(ix *agentconfig.Intercept) bool { return ix.ServiceName == svcName }) {
			css = append(css, c.Name)
		}
	}
	return css, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
}
