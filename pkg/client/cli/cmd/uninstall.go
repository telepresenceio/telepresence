package cmd

import (
	"errors"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/progress"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

const allAgentsFlag = "all-agents"

type uninstallCommand struct {
	agent     bool
	allAgents bool
}

func uninstall() *cobra.Command {
	ui := &uninstallCommand{}
	cmd := &cobra.Command{
		Use:   "uninstall [flags] <workloads...>",
		Args:  ui.args,
		Short: "Uninstall telepresence agents",
		RunE:  ui.run,
		Annotations: map[string]string{
			ann.Session: ann.Required,
		},
		ValidArgsFunction: validWorkloads,
	}
	flags := cmd.Flags()
	flags.BoolVarP(&ui.allAgents, allAgentsFlag, "a", false, "uninstall intercept agent on all workloads")

	// Hidden from help but will yield a deprecation warning if used
	flags.BoolVarP(&ui.agent, "agent", "d", false, "")
	flags.Lookup("agent").Hidden = true
	return cmd
}

func (u *uninstallCommand) args(_ *cobra.Command, args []string) error {
	if len(args) > 0 {
		if u.allAgents {
			return errors.New("--all-agents cannot be used with additional arguments")
		}
	} else if !u.allAgents {
		return errors.New("please specify at least one workload or use or --all-agents")
	}
	return nil
}

// uninstall.
func (u *uninstallCommand) run(cmd *cobra.Command, args []string) error {
	if u.agent {
		ioutil.Println(cmd.OutOrStderr(), "--agent is deprecated (it's the default, so the flag has no effect)")
	}
	if err := connect.InitCommand(cmd); err != nil {
		return err
	}
	defer progress.Stop(cmd.Context())
	ur := &connector.UninstallRequest{
		UninstallType: 0,
	}
	if u.allAgents {
		ur.UninstallType = connector.UninstallRequest_ALL_AGENTS
	} else {
		ur.UninstallType = connector.UninstallRequest_NAMED_AGENTS
		ur.Agents = args
	}
	ctx := cmd.Context()
	_, err := daemon.MustGetUserClient(ctx).Uninstall(ctx, ur)
	return grpc.FromGRPC(err)
}

func validWorkloads(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	// Trace level is used here, because we generally don't want to log expansion attempts
	// in the cli.log
	clog.Tracef(cmd.Context(), "toComplete = %s, args = %v", toComplete, args)

	all, _ := cmd.Flags().GetBool(allAgentsFlag)
	if all {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	if err := connect.InitCommand(cmd); err != nil {
		clog.Debug(cmd.Context(), err)
		return nil, cobra.ShellCompDirectiveError
	}
	req := connector.ListRequest{
		Filter: connector.ListRequest_INSTALLED_AGENTS,
	}
	ctx := cmd.Context()

	r, err := daemon.MustGetUserClient(ctx).List(ctx, &req)
	if err != nil {
		clog.Debugf(ctx, "unable to get list of workloads with agents: %v", err)
		return nil, cobra.ShellCompDirectiveError
	}

	list := make([]string, 0)
	for _, w := range r.Workloads {
		// only suggest strings that start with the string were autocompleting
		if strings.HasPrefix(w.Name, toComplete) && !slices.Contains(args, w.Name) {
			list = append(list, w.Name)
		}
	}
	return list, cobra.ShellCompDirectiveNoFileComp
}
