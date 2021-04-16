package cli

import (
	"context"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
)

type uninstallInfo struct {
	sessionInfo
	agent      bool
	allAgents  bool
	everything bool
	namespace  string
}

func uninstallCommand() *cobra.Command {
	ui := &uninstallInfo{}
	cmd := &cobra.Command{
		Use:  "uninstall [flags] { --agent <agents...> |--all-agents | --everything }",
		Args: ui.args,

		Short: "Uninstall telepresence agents and manager",
		RunE:  ui.run,
	}
	flags := cmd.Flags()

	flags.BoolVarP(&ui.agent, "agent", "d", false, "uninstall intercept agent on specific deployments")
	flags.BoolVarP(&ui.allAgents, "all-agents", "a", false, "uninstall intercept agent on all deployments")
	flags.BoolVarP(&ui.everything, "everything", "e", false, "uninstall agents and the traffic manager")
	flags.StringVarP(&ui.namespace, "namespace", "n", "", "If present, the namespace scope for this CLI request")

	return cmd
}

func (u *uninstallInfo) args(cmd *cobra.Command, args []string) error {
	if u.agent && u.allAgents || u.agent && u.everything || u.allAgents && u.everything {
		return errors.New("--agent, --all-agents, or --everything are mutually exclusive")
	}
	if !(u.agent || u.allAgents || u.everything) {
		return errors.New("please specify --agent, --all-agents, or --everything")
	}
	switch {
	case u.agent && len(args) == 0:
		return errors.New("at least one argument (the name of an agent) is expected")
	case !u.agent && len(args) != 0:
		return errors.New("unexpected argument(s)")
	}
	return nil
}

// uninstall
func (u *uninstallInfo) run(cmd *cobra.Command, args []string) error {
	doQuit := false
	err := cliutil.WithConnector(cmd.Context(), func(ctx context.Context, connectorClient connector.ConnectorClient) error {
		req := &connector.UninstallRequest{
			Namespace: u.namespace,
		}
		switch {
		case u.agent:
			req.UninstallType = connector.UninstallRequest_NAMED_AGENTS
			req.Agents = args
		case u.allAgents:
			req.UninstallType = connector.UninstallRequest_ALL_AGENTS
		default:
			req.UninstallType = connector.UninstallRequest_EVERYTHING
			// No need to keep daemons once everything is uninstalled.
			doQuit = true
		}
		resp, err := connectorClient.Uninstall(ctx, req)
		if err != nil {
			return err
		}
		if resp.ErrorText != "" {
			return errors.New(resp.ErrorText)
		}

		return nil
	})
	if err != nil {
		return err
	}
	if doQuit {
		u.cmd = cmd
		err := u.withConnector(true, func(cs *connectorState) error {
			return cs.removeClusterFromUserCache(cmd.Context())
		})
		if err != nil {
			return err
		}
		if err := quit(cmd, nil); err != nil {
			return err
		}
	}
	return nil
}

func (cs *connectorState) removeClusterFromUserCache(ctx context.Context) (err error) {
	// Login token is affined to the traffic-manager that just got removed. The user-info
	// in turn, is info obtained using that token so both are removed here as a
	// consequence of removing the manager.
	if err := cliutil.EnsureLoggedOut(ctx); err != nil {
		return err
	}

	// Delete the ingress info for the cluster if it exists.
	ingresses, err := cache.LoadIngressesFromUserCache(ctx)
	if err != nil {
		return err
	}

	key := cs.info.ClusterServer + "/" + cs.info.ClusterContext
	if _, ok := ingresses[key]; ok {
		delete(ingresses, key)
		if err = cache.SaveIngressesToUserCache(ctx, ingresses); err != nil {
			return err
		}
	}
	return nil
}
