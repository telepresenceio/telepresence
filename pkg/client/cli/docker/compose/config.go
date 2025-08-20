package compose

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/go-json-experiment/json"
	"github.com/puzpuzpuz/xsync/v4"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/cmd/cobraparser/generate"
	"github.com/telepresenceio/telepresence/cmd/cobraparser/types"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/flags"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/global"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/progress"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

const (
	extensionKey = "x-tele"
)

type parentConfig struct {
	*topLevelExtension

	// Compose options
	projectDir  string
	projectName string
	progress    string
	configPaths []string
	envFiles    []string
	profiles    []string
	services    []string

	commandFlags *pflag.FlagSet
}

type config struct {
	topLevelExtension
	*parentConfig
	subCommandFlags *pflag.FlagSet
}

func GenerateSubCommands(cmd *cobra.Command) []*cobra.Command {
	pc := &parentConfig{}
	uf := cmd.UsageFunc()
	cmd.SetUsageFunc(func(*cobra.Command) error {
		cmd.SetContext(flags.WithFlagSets(cmd.Context(), pc.commandFlags))
		return uf(cmd)
	})
	pc.addComposeFlags(cmd)
	commands := make([]*cobra.Command, len(dockerComposeCLI.Subcommands))
	for i, subCmd := range dockerComposeCLI.Subcommands {
		c := &config{parentConfig: pc}
		sc := c.subCommand(&subCmd)
		if dryRun := sc.Flags().Lookup("dry-run"); dryRun != nil {
			dryRun.Hidden = true
		}
		commands[i] = sc
	}
	return commands
}

// toProjectOptions was shamelessly copied from https://github.com/docker/compose/blob/main/cmd/compose/compose.go.
// Kudos to the Docker Compose CLI authors.
func (pc *parentConfig) toProjectOptions(po ...cli.ProjectOptionsFn) (*cli.ProjectOptions, error) {
	return cli.NewProjectOptions(pc.configPaths,
		append(po,
			cli.WithWorkingDirectory(pc.projectDir),
			// First, apply os.Environment, always win.
			cli.WithOsEnv,
			// Load PWD/.env if present and no explicit --env-file has been set.
			cli.WithEnvFiles(pc.envFiles...),
			// read the dot-env file to populate the project environment
			cli.WithDotEnv,
			// get the compose-file path set by COMPOSE_FILE
			cli.WithConfigFileEnv,
			// if none was selected, get the default compose.yaml file from the current dir or parent folder
			cli.WithDefaultConfigPath,
			// ... and then, a project directory != PWD maybe has been set, so let's load the .env file
			cli.WithEnvFiles(pc.envFiles...),
			cli.WithDotEnv,
			// eventually COMPOSE_PROFILES should have been set
			cli.WithDefaultProfiles(pc.profiles...),
			cli.WithName(pc.projectName))...)
}

var dockerComposeCLI types.CommandInfo //nolint:gochecknoglobals // this is a constant

//go:embed dc-cli.json
var dcCli []byte

func init() {
	err := json.Unmarshal(dcCli, &dockerComposeCLI)
	if err != nil {
		panic(err)
	}
}

func (pc *parentConfig) addComposeFlags(cmd *cobra.Command) {
	cfs := cmd.Flags()
	cFlags := pflag.NewFlagSet("Compose flags", pflag.ContinueOnError)
	cFlags.StringArrayVarP(&pc.configPaths, "file", "f", []string{}, "Compose configuration files")
	cFlags.StringArrayVar(&pc.envFiles, "env-file", []string{}, "Optional environment files")
	cFlags.StringVar(&pc.projectDir, "project-directory", "", "Specify an alternate working directory (default: the path of the, first specified, Compose file)")
	cFlags.StringVar(&pc.projectName, "project-name", "", "Project name")
	cFlags.StringArrayVar(&pc.profiles, "profile", []string{}, "Profile to enable")
	cfs.AddFlagSet(cFlags)
	uf := cmd.UsageFunc()
	cmd.SetUsageFunc(func(*cobra.Command) error {
		cmd.SetContext(flags.WithFlagSets(cmd.Context(), cFlags))
		return uf(cmd)
	})
	pc.commandFlags = cFlags
}

func (c *config) subCommand(subCmd *types.CommandInfo) *cobra.Command {
	cmd := &cobra.Command{
		Use:   fmt.Sprintf("%s [flags] [services]", subCmd.Name),
		Args:  cobra.ArbitraryArgs,
		Short: subCmd.Description,
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.run(cmd, args)
		},
		ValidArgsFunction: func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			dir := cobra.ShellCompDirectiveNoFileComp
			if slices.Contains(os.Args, "--") {
				dir = cobra.ShellCompDirectiveDefault
			}
			return nil, dir
		},
	}
	c.subCommandFlags = generate.FlagSet(fmt.Sprintf("Compose %s flags", subCmd.Name), subCmd)
	cmdFlags := cmd.Flags()
	err := flags.AddUnique(cmdFlags, c.subCommandFlags)
	if err != nil {
		ioutil.Printf(os.Stderr, "internal error: %v\n", err)
		os.Exit(1)
	}
	uf := cmd.UsageFunc()
	cmd.SetUsageFunc(func(*cobra.Command) error {
		cmd.SetContext(flags.WithFlagSets(cmd.Context(), c.commandFlags, c.subCommandFlags))
		return uf(cmd)
	})
	return cmd
}

func (c *config) loadProject(ctx context.Context) (*transformer, error) {
	options, err := c.toProjectOptions()
	if err != nil {
		return nil, err
	}
	p, err := options.LoadProject(ctx)
	if err != nil {
		return nil, err
	}
	ev, ok := p.Extensions[extensionKey]
	if ok {
		err = c.topLevelExtension.parse(ev)
		if err != nil {
			return nil, err
		}
	}
	return newTransformer(c, p)
}

func (c *config) appendFlags(flags *pflag.FlagSet, opts []string) []string {
	// Need VisitAll here because Visit doesn't use the Changed status of the actual flag, instead
	// it keeps track of flags set in the command's FlagSet.
	flags.VisitAll(func(f *pflag.Flag) {
		if !f.Changed {
			return
		}
		fv := f.Value
		switch fv.Type() {
		case "bool":
			if fv.String() == "true" {
				opts = append(opts, "--"+f.Name)
			}
		case "stringArray":
			sv := fv.(pflag.SliceValue)
			opt := "--" + f.Name
			for _, v := range sv.GetSlice() {
				opts = append(opts, opt, v)
			}
		default:
			opts = append(opts, "--"+f.Name, fv.String())
		}
	})
	return opts
}

func dispatchToCompose(ctx context.Context, name string, args []string) error {
	return proc.StdCommand(ctx, docker.Exe, slices.Insert(args, 0, "compose", name)...).Run()
}

func (c *config) run(cmd *cobra.Command, args []string) (err error) {
	defer func() {
		err = errcat.NoDaemonLogs.New(err)
	}()

	if dryFlag := cmd.Flag("dry-run"); dryFlag != nil && dryFlag.Changed {
		// A dry-run is impossible, because Telepresence will have to engage with a workload to get
		// the data needed to modify the docker compose project. The intercept, replace, ingest, and
		// wiretap will all install a traffic-agent and cannot be considered a dry-run.
		return errcat.User.New("--dry-run is not supported")
	}

	c.progress = cmd.Flag(global.FlagProgress).Value.String()
	ctx := cmd.Context()
	name := cmd.Name()
	connMustExist := true
	switch name {
	case "up", "create":
		c.services = args
		connMustExist = false
	case "build":
		connMustExist = false
	case "ls", "version":
		return dispatchToCompose(ctx, name, args)
	}

	ctx, err = daemon.WithDefaultRequest(cmd)
	if err != nil {
		return err
	}
	w := progress.NewWriter(cmd.OutOrStdout(), cmd.ErrOrStderr(), progress.Mode(c.progress))

	// Tell the underlying framework to keep quiet.
	ctx = progress.WithContextWriter(ctx, w)
	defer func() {
		progress.Stop(ctx)
	}()

	tr, err := c.loadProject(ctx)
	if err != nil {
		return err
	}
	if len(c.profiles) > 0 {
		err = tr.withProfiles(c.profiles)
		if err != nil {
			return err
		}
	}

	es := tr.serviceExtensions()
	dlog.Debugf(ctx, "Found %d engagements", len(es))
	if len(es) == 0 {
		return tr.runCommand(ctx, name, args)
	}

	aesCh := make(chan *engagement, len(es))
	connections := xsync.NewMap[string, *connection]()

	if name == "down" {
		defer func() {
			progress.Start(ctx, "Disconnecting")
			connections.Range(func(_ string, cc *connection) bool {
				cc.disconnect()
				return true
			})
		}()
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	progress.Start(ctx, "Connecting")
	for _, e := range es {
		var cc *connectionConfig
		cc, err = c.getConnectionConfig(e.connectionName())
		if err != nil {
			return err
		}
		c, _ := connections.LoadOrCompute(cc.Name, func() (*connection, bool) {
			var conn *connection
			conn, err = cc.Connect(ctx, es, connMustExist)
			return conn, err != nil
		})
		if err != nil {
			if connMustExist && errors.Is(err, connect.ErrNoUserDaemon) {
				// The daemon is not running, so no services should be running either. This is OK. We can just run the command without engagements.
				errBuf := bytes.Buffer{}
				err = tr.runCommand(dos.WithStderr(ctx, &errBuf), name, args)
			}
			return err
		}
		dlog.Debugf(ctx, "Service %q will be %s", e.composeService().Name, e.engagementType().WorkDone())
		e.setConnection(c)
	}

	g := dgroup.NewGroup(ctx, dgroup.GroupConfig{
		EnableSignalHandling: true,
	})

	for _, e := range es {
		progress.Start(ctx, "Engaging")
		cn := e.composeService().Name
		g.Go(cn, func(ctx context.Context) (err error) {
			ctx = progress.WithEventId(ctx, cn)
			progress.Working(ctx, e.engagementType().Working(), cn)
			var ae *engagement
			if connMustExist {
				ae, err = e.engaged()
			} else {
				ae, err = e.activate(tr.volumes())
			}
			if err != nil {
				return progress.MaybeWriteError(ctx, err)
			}
			progress.Done(ctx, e.engagementType().WorkDone(), cn)
			aesCh <- ae
			return nil
		})
	}

	g.Go("compose", func(ctx context.Context) error {
		for i := len(es); i > 0; i-- {
			select {
			case <-ctx.Done():
				return nil
			case ae := <-aesCh:
				tr.addEngagement(ae)
			}
		}
		progress.Stop(ctx)
		if name == "stop" || name == "up" && !flags.HasOption("detached", 'd', args) {
			defer func() {
				progress.Start(ctx, "Disengaging")
				for _, n := range maps.SortedKeys(tr.engagements) {
					e := tr.engagements[n]
					eCtx := progress.WithEventId(ctx, n)
					progress.Working(eCtx, e.engagementType().Leaving())
					err = e.deactivate()
					if err != nil {
						dlog.Error(eCtx, err)
					}
					progress.Done(eCtx, e.engagementType().Left())
				}
				progress.Stop(ctx)
			}()
		}
		return tr.runCommand(ctx, name, args)
	})
	err = g.Wait()
	if err != nil && strings.Contains(err.Error(), "graceful shutdown") {
		err = nil
	}
	return err
}

//nolint:gochecknoglobals // constant
var defaultConnectionConfig = &connectionConfig{
	Namespace: "default",
}

func (c *config) getConnectionConfig(name string) (*connectionConfig, error) {
	if len(c.Connections) == 0 {
		c.Connections = []*connectionConfig{defaultConnectionConfig}
	}
	ccs := c.Connections
	if name == "" {
		if len(ccs) == 1 {
			return ccs[0], nil
		}
		return nil, fmt.Errorf("multiple connections found, please specify a connection name")
	}
	for _, cc := range ccs {
		if cc.Name == name {
			return cc, nil
		}
	}
	return nil, fmt.Errorf("connection %q not found", name)
}
