package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

type listInfo struct {
	onlyIntercepts    bool
	onlyAgents        bool
	onlyInterceptable bool
	debug             bool
	namespace         string
	json              bool
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
	flags.BoolVarP(&s.onlyInterceptable, "only-interceptable", "o", true, "interceptable workloads only")
	flags.BoolVar(&s.debug, "debug", false, "include debugging information")
	flags.StringVarP(&s.namespace, "namespace", "n", "", "If present, the namespace scope for this CLI request")
	flags.BoolVarP(&s.json, "json", "j", false, "output as json array")
	return cmd
}

// list requests a list current intercepts from the daemon
func (s *listInfo) list(cmd *cobra.Command, _ []string) error {
	var r *connector.WorkloadInfoSnapshot
	var err error
	err = withConnector(cmd, true, nil, func(ctx context.Context, cs *connectorState) error {
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
		r, err = cs.userD.List(ctx, &connector.ListRequest{Filter: filter, Namespace: s.namespace})
		return err
	})
	if err != nil {
		return err
	}
	stdout := cmd.OutOrStdout()
	if len(r.Workloads) == 0 {
		if s.json {
			fmt.Fprintln(stdout, "[]")
		} else {
			fmt.Fprintln(stdout, "No Workloads (Deployments, StatefulSets, or ReplicaSets)")
		}
		return nil
	}

	state := func(workload *connector.WorkloadInfo) string {
		if ii := workload.InterceptInfo; ii != nil {
			return DescribeIntercept(ii, nil, s.debug)
		}
		ai := workload.AgentInfo
		if ai != nil {
			return "ready to intercept (traffic-agent already installed)"
		}
		if workload.NotInterceptableReason != "" {
			return "not interceptable (traffic-agent not installed): " + workload.NotInterceptableReason
		} else {
			return "ready to intercept (traffic-agent not yet installed)"
		}
	}

	if s.json {
		msg, err := json.Marshal(r.Workloads)
		if err != nil {
			fmt.Fprintf(stdout, "json marshal error: %v", err)
		} else {
			fmt.Fprintf(stdout, "%s", msg)
		}
	} else {
		includeNs := false
		ns := s.namespace
		for _, dep := range r.Workloads {
			depNs := dep.Namespace
			if depNs == "" {
				// Local-only, so use namespace of intercept
				depNs = dep.InterceptInfo.Spec.Namespace
			}
			if ns != "" && depNs != ns {
				includeNs = true
				break
			}
			ns = depNs
		}
		nameLen := 0
		for _, dep := range r.Workloads {
			n := dep.Name
			if n == "" {
				// Local-only, so use name of intercept
				n = dep.InterceptInfo.Spec.Name
			}
			nl := len(n)
			if includeNs {
				nl += len(dep.Namespace) + 1
			}
			if nl > nameLen {
				nameLen = nl
			}
		}
		for _, workload := range r.Workloads {
			if workload.Name == "" {
				// Local-only, so use name of intercept
				n := workload.InterceptInfo.Spec.Name
				if includeNs {
					n += "." + workload.Namespace
				}
				fmt.Fprintf(stdout, "%-*s: local-only intercept\n", nameLen, n)
			} else {
				n := workload.Name
				if includeNs {
					n += "." + workload.Namespace
				}
				fmt.Fprintf(stdout, "%-*s: %s\n", nameLen, n, state(workload))
			}
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
	fields = append(fields, kv{"Workload kind", ii.Spec.WorkloadKind})

	if debug {
		fields = append(fields, kv{"ID", ii.Id})
	}

	fields = append(fields, kv{"Destination",
		net.JoinHostPort(ii.Spec.TargetHost, fmt.Sprintf("%d", ii.Spec.TargetPort))})

	if ii.Spec.ServicePortIdentifier != "" {
		fields = append(fields, kv{"Service Port Identifier", ii.Spec.ServicePortIdentifier})
	}
	if debug {
		fields = append(fields, kv{"Mechanism", ii.Spec.Mechanism})
		fields = append(fields, kv{"Mechanism Args", fmt.Sprintf("%q", ii.Spec.MechanismArgs)})
		fields = append(fields, kv{"Metadata", fmt.Sprintf("%q", ii.Metadata)})
	}

	if ii.ClientMountPoint != "" {
		fields = append(fields, kv{"Volume Mount Point", ii.ClientMountPoint})
	} else if volumeMountsPrevented != nil {
		fields = append(fields, kv{"Volume Mount Error", volumeMountsPrevented.Error()})
	}

	fields = append(fields, kv{"Intercepting", func() string {
		if ii.MechanismArgsDesc == "" {
			if len(ii.Spec.MechanismArgs) > 0 {
				return fmt.Sprintf("using mechanism=%q with args=%q", ii.Spec.Mechanism, ii.Spec.MechanismArgs)
			}
			return fmt.Sprintf("using mechanism=%q", ii.Spec.Mechanism)
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
