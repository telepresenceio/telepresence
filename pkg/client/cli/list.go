package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/datawire/telepresence2/pkg/rpc/connector"
)

type listInfo struct {
	sessionInfo
	onlyIntercepts    bool
	onlyAgents        bool
	onlyInterceptable bool
}

func listCommand() *cobra.Command {
	s := &listInfo{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List current intercepts",
		Args:  cobra.NoArgs,
		RunE:  s.list,
	}
	flags := cmd.Flags()
	flags.BoolVarP(&s.onlyIntercepts, "intercepts", "i", false, "intercepts only")
	flags.BoolVarP(&s.onlyAgents, "agents", "a", false, "with installed agents only")
	flags.BoolVarP(&s.onlyInterceptable, "only-interceptable", "o", true, "interceptable deployments only")
	return cmd
}

// list requests a list current intercepts from the daemon
func (s *listInfo) list(cmd *cobra.Command, _ []string) error {
	var r *connector.DeploymentInfoSnapshot
	var err error
	s.cmd = cmd
	err = s.withConnector(true, func(cs *connectorState) error {
		var filter connector.ListRequest_Filter
		switch {
		case s.onlyIntercepts:
			filter = connector.ListRequest_INTERCEPTS
		case s.onlyAgents:
			filter = connector.ListRequest_INSTALLED_AGENTS
		case s.onlyInterceptable:
			filter = connector.ListRequest_INTERCEPTABLE
		default:
			filter = connector.ListRequest_EVERYTHING
		}
		r, err = cs.grpc.List(cmd.Context(), &connector.ListRequest{Filter: filter})
		return err
	})
	if err != nil {
		return err
	}
	stdout := cmd.OutOrStdout()
	if len(r.Deployments) == 0 {
		fmt.Fprintln(stdout, "No deployments")
		return nil
	}

	nameLen := 0
	for _, dep := range r.Deployments {
		if nl := len(dep.Name); nl > nameLen {
			nameLen = nl
		}
	}

	state := func(dep *connector.DeploymentInfo) string {
		if ii := dep.InterceptInfo; ii != nil {
			return fmt.Sprintf("intercepted, redirecting to %s:%d", ii.Spec.TargetHost, ii.Spec.TargetPort)
		}
		ai := dep.AgentInfo
		if ai != nil {
			return "traffic-agent installed"
		}
		txt := "traffic-agent not installed"
		if dep.NotInterceptableReason != "" {
			txt = txt + ": " + dep.NotInterceptableReason
		}
		return txt
	}

	for _, dep := range r.Deployments {
		fmt.Fprintf(stdout, "%-*s: %s\n", nameLen, dep.Name, state(dep))
	}
	return nil
}
