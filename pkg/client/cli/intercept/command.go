package intercept

import (
	"errors"
	"fmt"
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
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/global"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ingest"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/mount"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/progress"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
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
	Wiretap bool // wiretap subcommand used

	ToPod []string // --to-pod

	Cmdline []string // Command[1:]

	Mechanism       string // --mechanism tcp
	MechanismArgs   []string
	ExtendedInfo    []byte
	WaitMessage     string // Message printed when a containerized intercept handler is started and waiting for an interrupt
	FormattedOutput bool
	DetailedOutput  bool
	NoDefaultPort   bool

	// HTTP Intercepts fields
	HTTPHeaderFilters     []string // --http-header key=value pairs for HTTP header filtering
	HTTPPathEqualFilters  []string // --http-path-equal paths for HTTP path filtering (exact match)
	HTTPPathPrefixFilters []string // --http-path-prefix paths for HTTP path filtering (prefix match)
	HTTPPathRegexFilters  []string // --http-path-regex paths for HTTP path filtering (regex match)
}

// UsesHTTPMechanism returns true if any HTTP-specific flags were provided,
// indicating that HTTP-aware interception should be used.
func (c *Command) UsesHTTPMechanism() bool {
	return len(c.HTTPHeaderFilters) > 0 ||
		len(c.HTTPPathEqualFilters) > 0 ||
		len(c.HTTPPathPrefixFilters) > 0 ||
		len(c.HTTPPathRegexFilters) > 0
}

// parseHTTPHeader parses an HTTP header string that can use either "=" or ":" as separator.
// Supports both formats:
//   - "X-User-ID=dev123" (equals format)
//   - "X-User-ID: dev123" (colon format, compatible with curl -H)
//
// Returns the key and value, or an error if the format is invalid.
// When both separators are present, colon takes precedence (standard HTTP format).
func parseHTTPHeader(header string) (string, string, error) {
	// Try colon separator first (standard HTTP format, curl -H compatible)
	if key, value, ok := tryParseHeaderWithSeparator(header, ":"); ok {
		return key, value, nil
	}

	// Try equals separator
	if key, value, ok := tryParseHeaderWithSeparator(header, "="); ok {
		return key, value, nil
	}

	// Neither separator found
	return "", "", fmt.Errorf("invalid header format '%s': must be key=value or key: value", header)
}

// tryParseHeaderWithSeparator attempts to parse a header with the given separator.
// Returns the key, value, and true if successful; empty strings and false otherwise.
func tryParseHeaderWithSeparator(header, separator string) (string, string, bool) {
	parts := strings.SplitN(header, separator, 2)
	if len(parts) != 2 {
		return "", "", false
	}

	key := strings.TrimSpace(parts[0])
	if key == "" {
		return "", "", false
	}

	value := strings.TrimSpace(parts[1])
	return key, value, true
}

func (c *Command) AddInterceptFlags(cmd *cobra.Command) {
	what := "intercept"
	how := "intercepted"
	if c.Wiretap {
		what = "wiretap"
		how = "wiretapped"
	}
	flagSet := cmd.Flags()
	flagSet.StringVarP(&c.AgentName, "workload", "w", "", fmt.Sprintf("Name of workload (Deployment, ReplicaSet, StatefulSet, Rollout) to %s, if different from <name>", what))
	flagSet.StringSliceVarP(&c.Ports, "port", "p", nil, ``+
		`Local ports to forward to. Use <local port>:<identifier> to uniquely identify service ports, where the <identifier> is the port name or number. `+
		`With --docker-run and a daemon that doesn't run in docker', use <local port>:<container port> or `+
		`<local port>:<container port>:<identifier>.`,
	)

	flagSet.StringVar(&c.Address, "address", "", ``+
		`Local address to forward to, e.g. '--address 10.0.0.2' (default "127.0.0.1" or name of container)`,
	)

	flagSet.StringVar(&c.ServiceName, "service", "", fmt.Sprintf("Optional name of service to %s. Sometimes needed to uniquely identify the intercepted port.", what))

	flagSet.StringVar(&c.ContainerName, "container", "",
		fmt.Sprintf("Name of container that provides the environment and mounts for the %s. Defaults to the container matching the first %s port.", what, how))

	if !c.Wiretap {
		flagSet.StringSliceVar(&c.ToPod, "to-pod", []string{}, fmt.Sprintf(
			`Additional ports to forward to the %s pod, will available for connections to localhost:PORT. `+
				`Use this to, for example, access proxy/helper sidecars in the %s pod. The default protocol is TCP. `+
				`Use <port>/UDP for UDP ports`, how, how))
	}

	c.EnvFlags.AddFlags(flagSet)
	c.MountFlags.AddFlags(flagSet, false)
	c.DockerFlags.AddFlags(flagSet, how)

	flagSet.StringVar(&c.Mechanism, "mechanism", "tcp", "Which extension `mechanism` to use")

	flagSet.StringVar(&c.WaitMessage, "wait-message", "", fmt.Sprintf("Message to print when %s handler has started", what))

	flagSet.BoolVar(&c.DetailedOutput, "detailed-output", false,
		fmt.Sprintf(`Provide very detailed info about the %s when used together with --output=json or --output=yaml'`, what))

	if !c.Wiretap {
		flagSet.BoolVarP(&c.Replace, "replace", "", false,
			`Indicates if the traffic-agent should replace application containers in workload pods. `+
				`The default behavior is for the agent sidecar to be installed alongside existing containers.`)
		flagSet.Lookup("replace").Deprecated = "Use the replace command."
	}

	// HTTP Intercepts flags
	flagSet.StringSliceVar(&c.HTTPHeaderFilters, "http-header", nil,
		fmt.Sprintf(`HTTP header filters. Only requests with matching headers will be %s. `+
			`Supports both formats: --http-header "X-User-ID=dev123" or --http-header "X-User-ID: dev123" (curl -H compatible). `+
			`Multiple headers use AND logic.`, how))

	flagSet.StringSliceVar(&c.HTTPPathEqualFilters, "http-path-equal", nil,
		fmt.Sprintf(`HTTP path filters. Only requests with matching paths will be %s. `+
			`Exact path matching.`, how))

	flagSet.StringSliceVar(&c.HTTPPathPrefixFilters, "http-path-prefix", nil,
		fmt.Sprintf(`HTTP path prefix filters. Only requests with matching path prefixes will be %s.`, how))

	flagSet.StringSliceVar(&c.HTTPPathRegexFilters, "http-path-regex", nil,
		fmt.Sprintf(`HTTP path regex filters. Only requests with paths matching the regex will be %s.`, how))

	_ = cmd.RegisterFlagCompletionFunc("container", ingest.AutocompleteContainer)
	_ = cmd.RegisterFlagCompletionFunc("service", autocompleteService)
}

func (c *Command) AddReplaceFlags(cmd *cobra.Command) {
	flagSet := cmd.Flags()
	flagSet.StringSliceVarP(&c.Ports, "port", "p", []string{"all"}, ``+
		`Local ports to forward to. Use <local port>:<identifier> to uniquely identify container ports, where the <identifier> is the port name or number. `+
		`Use "all" (the default) to forward all ports declared in the replaced container to their corresponding local port. `,
	)

	flagSet.StringVar(&c.Address, "address", "", ``+
		`Local address to forward to, e.g. '--address 10.0.0.2' (default "127.0.0.1" or name of container)`,
	)

	flagSet.StringVar(&c.ContainerName, "container", "",
		"Name of container that should be replaced. Can be omitted if the workload only has one container.")

	flagSet.StringSliceVar(&c.ToPod, "to-pod", []string{}, ``+
		`Additional ports to forward to the pod containing the replaced container, will available for connections to localhost:PORT. `+
		`Use this to, for example, access proxy/helper sidecars in the pod. The default protocol is TCP. `+
		`Use <port>/UDP for UDP ports`)

	c.EnvFlags.AddFlags(flagSet)
	c.MountFlags.AddFlags(flagSet, false)
	c.DockerFlags.AddFlags(flagSet, "replaced")

	flagSet.StringVar(&c.WaitMessage, "wait-message", "", "Message to print when replace handler has started")

	flagSet.BoolVar(&c.DetailedOutput, "detailed-output", false,
		`Provide very detailed info about the replace when used together with --output=json or --output=yaml'`)

	_ = cmd.RegisterFlagCompletionFunc("container", ingest.AutocompleteContainer)
}

func (c *Command) Validate(cmd *cobra.Command, positional []string) error {
	if len(positional) > 1 && cmd.Flags().ArgsLenAtDash() != 1 {
		return errcat.User.New("commands to be run with intercept must come after options")
	}
	c.Name = positional[0]
	c.Cmdline = positional[1:]
	c.FormattedOutput = output.WantsFormatted(cmd)

	// HTTP Intercepts: validate header format
	if c.UsesHTTPMechanism() {
		for _, header := range c.HTTPHeaderFilters {
			if _, _, err := parseHTTPHeader(header); err != nil {
				return err
			}
		}

		// Validate HTTP mechanisms aren't used with UDP ports
		for _, portSpec := range c.Ports {
			if pp, err := types.ParsePortAndProto(portSpec); err == nil && pp.Proto == types.ProtoUDP {
				return errcat.User.Newf("HTTP filters cannot be used with UDP port %s", portSpec)
			}
		}

		// Auto-detect and set mechanism to "http"
		c.Mechanism = "http"
	}

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
	if c.Wiretap {
		c.MountFlags.ReadOnly = true
	}
	if c.FormattedOutput || c.EnvFlags.File == "-" {
		// Can't mix JSON or env output on stdout with progress monitor.
		global.SetProgressQuiet(cmd)
	}
	err := c.DockerFlags.Validate(c.Cmdline)
	if err != nil {
		return err
	}

	dlog.Debugf(cmd.Context(), "Docker flags = %v", c.DockerFlags)
	return nil
}

func (c *Command) ValidateReplace(cmd *cobra.Command, positional []string) error {
	if len(positional) > 1 && cmd.Flags().ArgsLenAtDash() != 1 {
		return errcat.User.New("commands to be run with replace must come after options")
	}
	c.Name = positional[0]
	c.AgentName = c.Name
	c.Cmdline = positional[1:]
	c.FormattedOutput = output.WantsFormatted(cmd)
	c.Mechanism = "tcp"
	c.Replace = true
	c.NoDefaultPort = true

	if c.ContainerName != "" {
		c.Name += "/" + c.ContainerName
	}

	if err := c.MountFlags.Validate(cmd); err != nil {
		return err
	}
	if c.DockerFlags.Mount != "" && !c.MountFlags.Enabled {
		return errors.New("--docker-mount cannot be used with --mount=false")
	}
	for i := range c.Ports {
		if c.Ports[i] == "all" {
			// Local port is unset, remote port is "all"
			c.Ports[i] = ":all"
		}
	}
	return c.DockerFlags.Validate(c.Cmdline)
}

func (c *Command) Run(cmd *cobra.Command, positional []string) error {
	err := c.Validate(cmd, positional)
	if err == nil {
		err = c.validatedRun(cmd)
	}
	return err
}

func (c *Command) RunReplace(cmd *cobra.Command, positional []string) error {
	err := c.ValidateReplace(cmd, positional)
	if err == nil {
		err = c.validatedRun(cmd)
	}
	return err
}

func (c *Command) validatedRun(cmd *cobra.Command) error {
	if err := connect.InitCommand(cmd); err != nil {
		return err
	}
	defer progress.Stop(cmd.Context())
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
	ctx := cmd.Context()

	r, err := daemon.MustGetUserClient(ctx).List(ctx, &connector.ListRequest{Filter: connector.ListRequest_UNSPECIFIED})
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

// BuildPathFilters builds a list of path filters from the given arguments.
// The filters are of the form:
//   - :path-equal:<path>
//   - :path-prefix:<path>
//   - :path-regex:<path>
func BuildPathFilters(equals, prefixes, regexps []string) []string {
	// Combine all path filters with their type prefixes
	allFilters := make([]string, len(equals)+len(prefixes)+len(regexps))

	// Add exact match filters
	i := 0
	for _, path := range equals {
		allFilters[i] = ":path-equal:" + path
		i++
	}

	// Add prefix match filters
	for _, path := range prefixes {
		allFilters[i] = ":path-prefix:" + path
		i++
	}

	// Add regex match filters
	for _, path := range regexps {
		allFilters[i] = ":path-regex:" + path
		i++
	}
	return allFilters
}
