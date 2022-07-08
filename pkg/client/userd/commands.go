package userd

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/extensions"
)

func GetCommands(ctx context.Context) cliutil.CommandGroups {
	var (
		s  service
		st = reflect.TypeOf(&s)
		sv = reflect.ValueOf(&s)

		ctxv = reflect.ValueOf(ctx)
		cg   = cliutil.CommandGroups{}
	)

	for i := 0; i < st.NumMethod(); i++ {
		m := st.Method(i)
		if !strings.HasPrefix(m.Name, "_cmd") {
			continue
		}
		cmdv := m.Func.Call([]reflect.Value{sv, ctxv})[0]
		cmd := cmdv.Interface().(*cobra.Command)
		annotations := cmd.Annotations
		if group, ok := annotations["cobra.commandGroup"]; ok {
			cmds := cg[group]
			if cmds == nil {
				cmds = []*cobra.Command{}
			}
			cmds = append(cmds, cmd)
			cg[group] = cmds
		}
	}

	return cg
}

func GetCommandsForLocal(ctx context.Context, err error) cliutil.CommandGroups {
	groups := GetCommands(ctx)
	for _, cmds := range groups {
		for _, cmd := range cmds {
			cmd.RunE = func(_ *cobra.Command, _ []string) error {
				// err here will be ErrNoUserDaemon "telepresence user daemon is not running"
				return fmt.Errorf("unable to run command: %w", err)
			}
		}
	}
	return groups
}

// GetCommands will return all commands implemented by the connector daemon.
func (s *service) _getCommands(ctx context.Context) cliutil.CommandGroups {
	return cliutil.CommandGroups{
		"test group": []*cobra.Command{s.interceptCommand(ctx)},
	}
}

// GetCommandsForLocal will return the same commands as GetCommands but in a non-runnable state that reports
// the error given. Should be used to build help strings even if it's not possible to connect to the connector daemon.
func (s *service) GetCommandsForLocal(ctx context.Context, err error) cliutil.CommandGroups {
	groups := s._getCommands(ctx)
	for _, cmds := range groups {
		for _, cmd := range cmds {
			cmd.RunE = func(_ *cobra.Command, _ []string) error {
				// err here will be ErrNoUserDaemon "telepresence user daemon is not running"
				return fmt.Errorf("unable to run command: %w", err)
			}
		}
	}
	return groups
}

func (s *service) interceptCommand(ctx context.Context) *cobra.Command {
	cmd := cobra.Command{
		Use:   "my-intercept",
		Short: "Intercept a service",
		Annotations: map[string]string{
			"cobra.commandGroup": "test group",
		},
		Args: cobra.MinimumNArgs(1),
	}

	iargs := interceptArgs{}
	flags := cmd.Flags()

	flags.StringVarP(&iargs.agentName, "workload", "w", "", "Name of workload (Deployment, ReplicaSet) to intercept, if different from <name>")
	flags.StringVarP(&iargs.port, "port", "p", strconv.Itoa(client.GetConfig(ctx).Intercept.DefaultPort), ``+
		`Local port to forward to. If intercepting a service with multiple ports, `+
		`use <local port>:<svcPortIdentifier>, where the identifier is the port name or port number. `+
		`With --docker-run, use <local port>:<container port> or <local port>:<container port>:<svcPortIdentifier>.`,
	)

	flags.StringVar(&iargs.serviceName, "service", "", "Name of service to intercept. If not provided, we will try to auto-detect one")

	flags.BoolVarP(&iargs.localOnly, "local-only", "l", false, ``+
		`Declare a local-only intercept for the purpose of getting direct outbound access to the intercept's namespace`)

	flags.BoolVarP(&iargs.previewEnabled, "preview-url", "u", cliutil.HasLoggedIn(ctx), ``+
		`Generate an edgestack.me preview domain for this intercept. `+
		`(default "true" if you are logged in with 'telepresence login', default "false" otherwise)`,
	)
	iargs.previewSpec = &manager.PreviewSpec{}
	addPreviewFlags("preview-url-", flags, iargs.previewSpec)

	flags.StringVarP(&iargs.envFile, "env-file", "e", "", ``+
		`Also emit the remote environment to an env file in Docker Compose format. `+
		`See https://docs.docker.com/compose/env-file/ for more information on the limitations of this format.`)

	flags.StringVarP(&iargs.envJSON, "env-json", "j", "", `Also emit the remote environment to a file as a JSON blob.`)

	flags.StringVarP(&iargs.mount, "mount", "", "true", ``+
		`The absolute path for the root directory where volumes will be mounted, $TELEPRESENCE_ROOT. Use "true" to `+
		`have Telepresence pick a random mount point (default). Use "false" to disable filesystem mounting entirely.`)

	flags.StringSliceVar(&iargs.toPod, "to-pod", []string{}, ``+
		`An additional port to forward from the intercepted pod, will be made available at localhost:PORT `+
		`Use this to, for example, access proxy/helper sidecars in the intercepted pod. The default protocol is TCP. `+
		`Use <port>/UDP for UDP ports`)

	flags.BoolVarP(&iargs.dockerRun, "docker-run", "", false, ``+
		`Run a Docker container with intercepted environment, volume mount, by passing arguments after -- to 'docker run', `+
		`e.g. '--docker-run -- -it --rm ubuntu:20.04 /bin/bash'`)

	flags.StringVarP(&iargs.dockerMount, "docker-mount", "", "", ``+
		`The volume mount point in docker. Defaults to same as "--mount"`)

	flags.StringVarP(&iargs.namespace, "namespace", "n", "", "If present, the namespace scope for this CLI request")

	flags.StringVar(&iargs.ingressHost, "ingress-host", "", "If this flag is set, the ingress dialogue will be skipped,"+
		" and this value will be used as the ingress hostname.")
	flags.Int32Var(&iargs.ingressPort, "ingress-port", 0, "If this flag is set, the ingress dialogue will be skipped,"+
		" and this value will be used as the ingress port.")
	flags.BoolVar(&iargs.ingressTLS, "ingress-tls", false, "If this flag is set, the ingress dialogue will be skipped."+
		" If the dialogue is skipped, this flag will determine if TLS is used, and will default to false.")
	flags.StringVar(&iargs.ingressL5, "ingress-l5", "", "If this flag is set, the ingress dialogue will be skipped,"+
		" and this value will be used as the L5 hostname. If the dialogue is skipped, this flag will default to the ingress-host value")

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		fmt.Fprintf(cmd.OutOrStdout(), "got args: %+v\n", iargs)

		return nil
	}

	return &cmd
}

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

	// ingress cmd inputs
	ingressHost string
	ingressPort int32
	ingressTLS  bool
	ingressL5   string
}

// addPreviewFlags mutates 'flags', adding flags to it such that the flags set the appropriate
// fields in the given 'spec'.  If 'prefix' is given, long-flag names are prefixed with it.
func addPreviewFlags(prefix string, flags *pflag.FlagSet, spec *manager.PreviewSpec) {
	flags.BoolVarP(&spec.DisplayBanner, prefix+"banner", "b", true, "Display banner on preview page")
}
