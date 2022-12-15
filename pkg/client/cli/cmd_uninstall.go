package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/util"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

type uninstallInfo struct {
	agent      bool
	allAgents  bool
	everything bool
	namespace  string
}

func uninstallCommand() *cobra.Command {
	ui := &uninstallInfo{}
	cmd := &cobra.Command{
		Use:  "uninstall [flags] { --agent <agents...> | --all-agents }",
		Args: ui.args,

		Short: "Uninstall telepresence agents",
		RunE:  ui.run,
		Annotations: map[string]string{
			ann.Session: ann.Required,
		},
	}
	flags := cmd.Flags()

	flags.BoolVarP(&ui.agent, "agent", "d", false, "uninstall intercept agent on specific deployments")
	flags.BoolVarP(&ui.allAgents, "all-agents", "a", false, "uninstall intercept agent on all deployments")
	flags.StringVarP(&ui.namespace, "namespace", "n", "", "If present, the namespace scope for this CLI request")

	// Hidden from help but will yield a deprecation warning if used
	flags.BoolVarP(&ui.everything, "everything", "e", false, "uninstall agents and the traffic manager")
	flags.Lookup("everything").Hidden = true
	return cmd
}

func (u *uninstallInfo) args(cmd *cobra.Command, args []string) error {
	if u.everything {
		ha := &helmArgs{
			cmdType: connector.HelmRequest_UNINSTALL,
			request: &connector.ConnectRequest{},
		}
		fmt.Fprintln(cmd.OutOrStderr(), "--everything is deprecated. Please use telepresence helm uninstall")
		return ha.run(cmd, args)
	}
	if u.agent && u.allAgents {
		return errors.New("--agent and --all-agents are mutually exclusive")
	}
	if !(u.agent || u.allAgents) {
		return errors.New("please specify --agent or --all-agents")
	}
	switch {
	case u.agent && len(args) == 0:
		return errors.New("at least one argument (the name of an agent) is expected")
	case !u.agent && len(args) != 0:
		return errors.New("unexpected argument(s)")
	}
	return nil
}

// uninstall.
func (u *uninstallInfo) run(cmd *cobra.Command, args []string) error {
	if err := util.InitCommand(cmd); err != nil {
		return err
	}
	ur := &connector.UninstallRequest{
		UninstallType: 0,
		Namespace:     u.namespace,
	}
	switch {
	case u.agent:
		ur.UninstallType = connector.UninstallRequest_NAMED_AGENTS
		ur.Agents = args
	default:
		ur.UninstallType = connector.UninstallRequest_ALL_AGENTS
	}
	ctx := cmd.Context()
	r, err := util.GetUserDaemon(ctx).Uninstall(ctx, ur)
	if err != nil {
		return err
	}
	return errcat.FromResult(r)
}
