package cmd

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

type uninstallCommand struct {
	agent      bool
	allAgents  bool
	everything bool
}

func uninstall() *cobra.Command {
	ui := &uninstallCommand{}
	cmd := &cobra.Command{
		Use:  "uninstall [flags] { --agent <agents...> | --all-agents }",
		Args: ui.args,

		Short: "Uninstall telepresence agents",
		RunE:  ui.run,
	}
	flags := cmd.Flags()

	flags.BoolVarP(&ui.agent, "agent", "d", false, "uninstall intercept agent on specific deployments")
	flags.BoolVarP(&ui.allAgents, "all-agents", "a", false, "uninstall intercept agent on all deployments")

	// Hidden from help but will yield a deprecation warning if used
	flags.BoolVarP(&ui.everything, "everything", "e", false, "uninstall agents and the traffic manager")
	flags.Lookup("everything").Hidden = true
	return cmd
}

func (u *uninstallCommand) args(cmd *cobra.Command, args []string) error {
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
func (u *uninstallCommand) run(cmd *cobra.Command, args []string) error {
	if u.everything {
		ha := &HelmCommand{
			Request: helm.Request{Type: helm.Uninstall},
			rq:      daemon.InitRequest(cmd),
		}
		ioutil.Println(cmd.OutOrStderr(), "--everything is deprecated. Please use telepresence helm uninstall")
		return ha.run(cmd, args)
	}
	cmd.Annotations = map[string]string{
		ann.Session: ann.Required,
	}
	if err := connect.InitCommand(cmd); err != nil {
		return err
	}
	ur := &connector.UninstallRequest{
		UninstallType: 0,
	}
	switch {
	case u.agent:
		ur.UninstallType = connector.UninstallRequest_NAMED_AGENTS
		ur.Agents = args
	case u.everything:
		return nil
	default:
		ur.UninstallType = connector.UninstallRequest_ALL_AGENTS
	}
	ctx := cmd.Context()
	r, err := daemon.GetUserClient(ctx).Uninstall(ctx, ur)
	if err != nil {
		return err
	}
	return errcat.FromResult(r)
}
