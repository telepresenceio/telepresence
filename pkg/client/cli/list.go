package cli

import (
	"fmt"
	"net"
	"strings"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

type listInfo struct {
	sessionInfo
	onlyIntercepts    bool
	onlyAgents        bool
	onlyInterceptable bool
	debug             bool
	namespace         string
}

func listCommand() *cobra.Command {
	s := &listInfo{}
	cmd := &cobra.Command{
		Use:  "list",
		Args: cobra.NoArgs,

		Short: "List current intercepts",
		RunE:  s.list,
	}
	flags := cmd.Flags()
	flags.BoolVarP(&s.onlyIntercepts, "intercepts", "i", false, "intercepts only")
	flags.BoolVarP(&s.onlyAgents, "agents", "a", false, "with installed agents only")
	flags.BoolVarP(&s.onlyInterceptable, "only-interceptable", "o", true, "interceptable deployments only")
	flags.BoolVar(&s.debug, "debug", false, "include debugging information")
	flags.StringVarP(&s.namespace, "namespace", "n", "", "If present, the namespace scope for this CLI request")
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
		r, err = cs.connectorClient.List(cmd.Context(), &connector.ListRequest{Filter: filter, Namespace: s.namespace})
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
		n := dep.Name
		if n == "" {
			// Local-only, so use name of intercept
			n = dep.InterceptInfo.Spec.Name
		}
		if nl := len(n); nl > nameLen {
			nameLen = nl
		}
	}

	state := func(dep *connector.DeploymentInfo) string {
		if ii := dep.InterceptInfo; ii != nil {
			return DescribeIntercept(ii, nil, s.debug)
		}
		ai := dep.AgentInfo
		if ai != nil {
			return "ready to intercept (traffic-agent already installed)"
		}
		if dep.NotInterceptableReason != "" {
			return "not interceptable (traffic-agent not installed): " + dep.NotInterceptableReason
		} else {
			return "ready to intercept (traffic-agent not yet installed)"
		}
	}

	for _, dep := range r.Deployments {
		if dep.Name == "" {
			// Local-only, so use name of intercept
			fmt.Fprintf(stdout, "%-*s: local-only intercept\n", nameLen, dep.InterceptInfo.Spec.Name)
		} else {
			fmt.Fprintf(stdout, "%-*s: %s\n", nameLen, dep.Name, state(dep))
		}
	}
	return nil
}

func DescribeIntercept(ii *manager.InterceptInfo, volumeMountsPrevented error, debug bool) string {
	msg := "intercepted"

	type kv struct {
		Key   string
		Value string
	}

	var fields []kv

	fields = append(fields, kv{"Intercept name", ii.Spec.Name})
	fields = append(fields, kv{"State", func() string {
		msg := ""
		if ii.Disposition > manager.InterceptDispositionType_WAITING {
			msg += "error: "
		}
		msg += ii.Disposition.String()
		if ii.Message != "" {
			msg += ": " + ii.Message
		}
		return msg
	}()})

	if debug {
		fields = append(fields, kv{"ID", ii.Id})
		fields = append(fields, kv{"Manager Port", fmt.Sprintf("%d", ii.ManagerPort)})
	}

	fields = append(fields, kv{"Destination",
		net.JoinHostPort(ii.Spec.TargetHost, fmt.Sprintf("%d", ii.Spec.TargetPort))})

	if ii.Spec.ServicePortName != "" {
		fields = append(fields, kv{"Service Port Name", ii.Spec.ServicePortName})
	}
	if debug {
		fields = append(fields, kv{"Mechanism", ii.Spec.Mechanism})
		fields = append(fields, kv{"Mechanism Args", fmt.Sprintf("%q", ii.Spec.MechanismArgs)})
	}

	if ii.Spec.MountPoint != "" {
		fields = append(fields, kv{"Volume Mount Point", ii.Spec.MountPoint})
	} else if volumeMountsPrevented != nil {
		fields = append(fields, kv{"Volume Mount Error", volumeMountsPrevented.Error()})
	}

	fields = append(fields, kv{"Intercepting", func() string {
		if ii.MechanismArgsDesc == "" {
			return fmt.Sprintf("using mechanism=%q with args=%q", ii.Spec.Mechanism, ii.Spec.MechanismArgs)
		}
		return ii.MechanismArgsDesc
	}()})

	if ii.PreviewDomain != "" {
		previewURL := ii.PreviewDomain
		// Right now SystemA gives back domains with the leading "https://", but
		// let's not rely on that.
		if !strings.HasPrefix(previewURL, "https://") && !strings.HasPrefix(previewURL, "http://") {
			previewURL = "https://" + previewURL
		}
		fields = append(fields, kv{"Preview URL", previewURL})
	}
	if l5Hostname := ii.GetPreviewSpec().GetIngress().GetL5Host(); l5Hostname != "" {
		fields = append(fields, kv{"Layer 5 Hostname", l5Hostname})
	}

	klen := 0
	for _, kv := range fields {
		if len(kv.Key) > klen {
			klen = len(kv.Key)
		}
	}
	for _, kv := range fields {
		vlines := strings.Split(strings.TrimSpace(kv.Value), "\n")
		msg += fmt.Sprintf("\n    %-*s: %s", klen, kv.Key, vlines[0])
		for _, vline := range vlines[1:] {
			msg += "\n      " + vline
		}
	}
	return msg
}
