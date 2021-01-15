package cli

import (
	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	"github.com/datawire/telepresence2/pkg/rpc/connector"
)

type uninstallInfo struct {
	sessionInfo
	agent      bool
	allAgents  bool
	everything bool
}

func uninstallCommand() *cobra.Command {
	ui := &uninstallInfo{}
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall telepresence agents and manager",
		RunE:  ui.uninstall,
	}
	flags := cmd.Flags()

	flags.BoolVarP(&ui.agent, "agent", "d", false, "uninstall intercept agent on specific deployments")
	flags.BoolVarP(&ui.allAgents, "all-agents", "a", false, "uninstall intercept agent on all deployments")
	flags.BoolVarP(&ui.everything, "everything", "e", false, "uninstall intercept agent on all deployments")
	return cmd
}

// uninstall
func (u *uninstallInfo) uninstall(cmd *cobra.Command, args []string) error {
	doQuit := false
	if u.agent && u.allAgents || u.agent && u.everything || u.allAgents && u.everything {
		return errors.New("--agent, --all-agents, or --everything are mutually exclusive")
	}
	if !(u.agent || u.allAgents || u.everything) {
		return errors.New("please specify --agent, --all-agents, or --everything")
	}

	u.cmd = cmd
	err := u.withConnector(true, func(cs *connectorState) error {
		ur := &connector.UninstallRequest{
			UninstallType: 0,
			Agents:        args,
		}
		switch {
		case u.agent:
			ur.UninstallType = connector.UninstallRequest_NAMED_AGENTS
			ur.Agents = args
			if len(args) == 0 {
				return errors.New("at least one argument (the name of an agent) is expected")
			}
		case u.allAgents:
			ur.UninstallType = connector.UninstallRequest_ALL_AGENTS
			if len(args) != 0 {
				return errors.New("unexpected argument(s)")
			}
		default:
			if len(args) != 0 {
				return errors.New("unexpected argument(s)")
			}
			ur.UninstallType = connector.UninstallRequest_EVERYTHING
		}
		r, err := cs.connectorClient.Uninstall(cmd.Context(), ur)
		if err != nil {
			return err
		}
		if r.ErrorText != "" {
			return errors.New(r.ErrorText)
		}

		// No need to keep daemons once everything is uninstalled
		doQuit = ur.UninstallType == connector.UninstallRequest_EVERYTHING
		return nil
	})
	if doQuit {
		err = quit(cmd, nil)
	}
	return err
}
