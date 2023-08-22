package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/intercept"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

type listCommand struct {
	onlyIntercepts    bool
	onlyAgents        bool
	onlyInterceptable bool
	debug             bool
	namespace         string
	watch             bool
}

type workloadJSONOutput struct {
	*connector.WorkloadInfo
	Sidecar *agentconfig.Sidecar `json:"sidecar,omitempty"`
}

func list() *cobra.Command {
	s := &listCommand{}
	cmd := &cobra.Command{
		Use:  "list",
		Args: cobra.NoArgs,

		Short: "List current intercepts",
		RunE:  s.list,
		Annotations: map[string]string{
			ann.Session: ann.Required,
		},
	}
	flags := cmd.Flags()
	flags.BoolVarP(&s.onlyIntercepts, "intercepts", "i", false, "intercepts only")
	flags.BoolVarP(&s.onlyAgents, "agents", "a", false, "with installed agents only")
	flags.BoolVarP(&s.onlyInterceptable, "only-interceptable", "o", true, "interceptable workloads only")
	flags.BoolVar(&s.debug, "debug", false, "include debugging information")
	flags.StringVarP(&s.namespace, "namespace", "n", "", "If present, the namespace scope for this CLI request")

	flags.BoolVarP(&s.watch, "watch", "w", false, "watch a namespace. --agents and --intercepts are disabled if this flag is set")
	wf := flags.Lookup("watch")
	wf.Hidden = true
	wf.Deprecated = `Use "--output json-stream" instead of "--watch"`

	_ = cmd.RegisterFlagCompletionFunc("namespace", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		shellCompDir := cobra.ShellCompDirectiveNoFileComp
		if err := connect.InitCommand(cmd); err != nil {
			shellCompDir |= cobra.ShellCompDirectiveError
			return nil, shellCompDir
		}
		ctx := cmd.Context()
		userD := daemon.GetUserClient(ctx)
		resp, err := userD.GetNamespaces(ctx, &connector.GetNamespacesRequest{
			ForClientAccess: false,
			Prefix:          toComplete,
		})
		if err != nil {
			dlog.Debugf(cmd.Context(), "error getting namespaces: %v", err)
			shellCompDir |= cobra.ShellCompDirectiveError
			return nil, shellCompDir
		}
		return resp.Namespaces, shellCompDir
	})
	return cmd
}

type watchWorkloadStreamResponse struct {
	workloadInfoSnapshot *connector.WorkloadInfoSnapshot
	err                  error
}

// list requests a list current intercepts from the daemon.
func (s *listCommand) list(cmd *cobra.Command, _ []string) error {
	if err := connect.InitCommand(cmd); err != nil {
		return err
	}
	stdout := cmd.OutOrStdout()
	ctx := cmd.Context()
	userD := daemon.GetUserClient(ctx)
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

	cfg := client.GetConfig(ctx)
	maxRecSize := int64(1024 * 1024 * 20) // Default to 20 Mb here. List can be quit long.
	if mz := cfg.Grpc().MaxReceiveSize(); mz > 0 {
		if mz > maxRecSize {
			maxRecSize = mz
		}
	}

	formattedOutput := output.WantsFormatted(cmd)
	if !output.WantsStream(cmd) {
		r, err := userD.List(ctx, &connector.ListRequest{Filter: filter, Namespace: s.namespace}, grpc.MaxCallRecvMsgSize(int(maxRecSize)))
		if err != nil {
			return err
		}
		s.printList(ctx, r.Workloads, stdout, formattedOutput)
		return nil
	}

	stream, streamErr := userD.WatchWorkloads(ctx, &connector.WatchWorkloadsRequest{Namespaces: []string{s.namespace}}, grpc.MaxCallRecvMsgSize(int(maxRecSize)))
	if streamErr != nil {
		return streamErr
	}

	ch := make(chan *watchWorkloadStreamResponse)
	go func() {
		for {
			snap, err := stream.Recv()
			ch <- &watchWorkloadStreamResponse{
				workloadInfoSnapshot: snap,
				err:                  err,
			}
			if err != nil {
				close(ch)
				break
			}
		}
	}()

	for {
		select {
		case r, ok := <-ch:
			if !ok {
				return nil
			}
			if r.err != nil {
				return errcat.NoDaemonLogs.Newf("%v", r.err)
			}
			s.printList(ctx, r.workloadInfoSnapshot.Workloads, stdout, formattedOutput)
		case <-ctx.Done():
			return nil
		}
	}
}

func (s *listCommand) printList(ctx context.Context, workloads []*connector.WorkloadInfo, stdout io.Writer, formattedOut bool) {
	if len(workloads) == 0 {
		if formattedOut {
			output.Object(ctx, []struct{}{}, false)
		} else {
			fmt.Fprintln(stdout, "No Workloads (Deployments, StatefulSets, or ReplicaSets)")
		}
		return
	}

	state := func(workload *connector.WorkloadInfo) string {
		if iis := workload.InterceptInfos; len(iis) > 0 {
			return intercept.DescribeIntercepts(ctx, iis, "", s.debug)
		}
		ai := workload.Sidecar
		if ai != nil {
			return "ready to intercept (traffic-agent already installed)"
		}
		if workload.NotInterceptableReason != "" {
			return "not interceptable (traffic-agent not installed): " + workload.NotInterceptableReason
		} else {
			return "ready to intercept (traffic-agent not yet installed)"
		}
	}

	if formattedOut {
		o := make([]*workloadJSONOutput, len(workloads))
		for i, v := range workloads {
			l := workloadJSONOutput{WorkloadInfo: v}

			if v.Sidecar != nil {
				var sidecar agentconfig.Sidecar
				_ = json.Unmarshal(v.Sidecar.Json, &sidecar)
				l.Sidecar = &sidecar
			}

			o[i] = &l
		}

		output.Object(ctx, o, false)
	} else {
		includeNs := false
		ns := s.namespace
		for _, dep := range workloads {
			depNs := dep.Namespace
			if depNs == "" {
				// Local-only, so use namespace of first intercept
				depNs = dep.InterceptInfos[0].Spec.Namespace
			}
			if ns != "" && depNs != ns {
				includeNs = true
				break
			}
			ns = depNs
		}
		nameLen := 0
		for _, dep := range workloads {
			n := dep.Name
			if n == "" {
				// Local-only, so use name of first intercept
				n = dep.InterceptInfos[0].Spec.Name
			}
			nl := len(n)
			if includeNs {
				nl += len(dep.Namespace) + 1
			}
			if nl > nameLen {
				nameLen = nl
			}
		}
		for _, workload := range workloads {
			if workload.Name == "" {
				// Local-only, so use name of first intercept
				n := workload.InterceptInfos[0].Spec.Name
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
}
