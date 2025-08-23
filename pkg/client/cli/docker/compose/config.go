package compose

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/loader"
	compose "github.com/compose-spec/compose-go/v2/types"
	"github.com/go-json-experiment/json"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/cmd/cobraparser/generate"
	"github.com/telepresenceio/telepresence/cmd/cobraparser/types"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/flags"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/global"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/progress"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
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

	mustBeConnected bool
	services        []string
	commandFlags    *pflag.FlagSet
	existingProject *compose.Project
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
			c.services = args
			return c.run(cmd)
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

func (c *config) detached() bool {
	if f := c.subCommandFlags.Lookup("detach"); f != nil {
		return f.Changed && f.Value.String() == "true"
	}
	return false
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
			v := "--" + f.Name
			if fv.String() == "false" {
				v += "=false"
			}
			opts = append(opts, v)
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

func (c *config) run(cmd *cobra.Command) (err error) {
	if dryFlag := cmd.Flag("dry-run"); dryFlag != nil && dryFlag.Changed {
		// A dry-run is impossible, because Telepresence will have to engage with a workload to get
		// the data needed to modify the docker compose project. The intercept, replace, ingest, and
		// wiretap will all install a traffic-agent and cannot be considered a dry-run.
		return errcat.User.New("--dry-run is not supported")
	}

	c.progress = cmd.Flag(global.FlagProgress).Value.String()
	name := cmd.Name()
	c.mustBeConnected = true
	switch name {
	case "build", "create", "start", "up":
		c.mustBeConnected = false
	case "ls", "version":
		return c.dispatchToCompose(cmd.Context(), name)
	}

	ctx, err := daemon.WithDefaultRequest(cmd)
	if err != nil {
		return err
	}

	// Any resources created here must be canceled when this function returns.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	w := progress.NewWriter(cmd.OutOrStdout(), cmd.ErrOrStderr(), progress.Mode(c.progress))

	// Tell the underlying framework to keep quiet.
	ctx = progress.WithContextWriter(ctx, w)
	defer func() {
		progress.Stop(ctx)
	}()

	tr, err := c.loadProject(ctx)
	if err != nil {
		return errcat.User.New(err)
	}
	es := tr.serviceExtensions()
	if len(es) == 0 {
		return tr.runCommand(ctx, name)
	}

	connections := make(map[string]*connection)
	if name == "down" {
		defer func() {
			progress.Start(ctx, "Disconnecting")
			for _, cc := range connections {
				cc.disconnect()
			}
		}()
	}

	progress.Start(ctx, "Connecting")
	existingComposeFile, err := c.connect(ctx, es, connections)
	if err != nil {
		if c.mustBeConnected && errors.Is(err, connect.ErrNoUserDaemon) {
			// The daemon is not running, although the command expects it to. This means that no services should be running either.
			// So let's just run the command without any extensions so that docker compose produces the expected error output.
			err = tr.runCommand(ctx, name)
		}
		return err
	}

	if existingComposeFile != "" {
		dlog.Debugf(ctx, "Existing compose file: %s", existingComposeFile)
		p, err := loadExistingProject(ctx, c.projectDir, existingComposeFile)
		if err != nil {
			return err
		}
		c.existingProject = p
	}
	g := dgroup.NewGroup(ctx, dgroup.GroupConfig{
		EnableSignalHandling: true,
	})
	aesCh := make(chan *engagement, len(es))
	progress.Start(ctx, "Engaging")
	for _, e := range es {
		tr.engage(g, e, aesCh)
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
		if name == "create" || name == "stop" || name == "up" && !c.detached() {
			defer tr.disengage(ctx)
		}
		return tr.runCommand(ctx, name)
	})
	err = g.Wait()
	if err != nil && strings.Contains(err.Error(), "graceful shutdown") {
		err = nil
	}
	return err
}

func (c *config) connect(ctx context.Context, es map[string]serviceExtension, connections map[string]*connection) (existingComposeFile string, err error) {
	for _, e := range es {
		var cc *connectionConfig
		cc, err = c.getConnectionConfig(e.connectionName())
		if err != nil {
			return "", err
		}
		cx, ok := connections[cc.Name]
		if !ok {
			cx, err = cc.Connect(ctx, es, c.mustBeConnected)
			if err != nil {
				return "", err
			}
			connections[cc.Name] = cx
		}
		dlog.Debugf(ctx, "Service %q will be %s", e.composeService().Name, e.engagementType().WorkDone())
		if existingComposeFile == "" {
			existingComposeFile = daemon.GetSession(cx).DaemonInfo().ComposeFile
		}
		e.setConnection(cx)
	}
	return existingComposeFile, nil
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
		err = c.parse(ev)
		if err != nil {
			return nil, err
		}
	}
	tr, err := newTransformer(c, p)
	if err != nil {
		return nil, err
	}
	if len(c.profiles) > 0 {
		err = tr.withProfiles(c.profiles)
		if err != nil {
			return nil, err
		}
	}
	return tr, nil
}

func (c *config) dispatchToCompose(ctx context.Context, name string) error {
	return errcat.User.New(proc.StdCommand(ctx, docker.Exe, slices.Insert(c.services, 0, "compose", name)...).Run())
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
		return nil, errcat.User.New("multiple connections found, please specify a connection name")
	}
	for _, cc := range ccs {
		if cc.Name == name {
			return cc, nil
		}
	}
	return nil, errcat.User.Newf("connection %q not found", name)
}

func (c *config) getMountPort(e mountsExtension) (uint16, error) {
	if !e.needsVolumes() {
		return 0, nil
	}
	if ep := c.existingProject; ep != nil {
		cn := e.composeService().Name
		if s, ok := ep.Services[cn]; ok {
			if pa, ok := s.Annotations[mountPortAnnotation]; ok {
				if p, err := strconv.Atoi(pa); err == nil {
					dlog.Debugf(e.connection(), "Found existing mount port %d for %q", p, cn)
					return uint16(p), nil
				}
			}
		}
	}
	lma, err := client.FreePortsTCP(1)
	if err != nil {
		return 0, err
	}
	return lma[0].Port(), nil
}

func loadExistingProject(ctx context.Context, pwd, path string) (*compose.Project, error) {
	return loader.LoadWithContext(ctx, compose.ConfigDetails{
		ConfigFiles: []compose.ConfigFile{{Filename: path}},
		WorkingDir:  pwd,
	}, func(options *loader.Options) {
		options.SkipConsistencyCheck = true
		options.SkipValidation = true
		options.SkipNormalization = true
		options.SkipInterpolation = true
		options.SkipResolveEnvironment = true
		options.SkipDefaultValues = true
		options.SkipExtends = true
		options.SkipInclude = true
	})
}
