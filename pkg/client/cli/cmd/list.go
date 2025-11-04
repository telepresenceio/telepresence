package cmd

import (
	"context"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/intercept"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/progress"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	tpGrpc "github.com/telepresenceio/telepresence/v2/pkg/grpc"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

const (
	includeIntercepts = iota
	includeIngests
	includeReplacements
	includeWiretaps
)

type listCommand struct {
	inclusions [4]bool
	onlyAgents bool
	debug      bool
	namespace  string
	watch      bool
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
		ValidArgsFunction: cobra.NoFileCompletions,
	}
	flags := cmd.Flags()
	flags.BoolVarP(&s.inclusions[includeIntercepts], "intercepts", "i", false, "intercepts")
	flags.BoolVarP(&s.inclusions[includeIngests], "ingests", "g", false, "ingests")
	flags.BoolVarP(&s.inclusions[includeReplacements], "replacements", "r", false, "replacements")
	flags.BoolVarP(&s.inclusions[includeWiretaps], "wiretaps", "t", false, "wiretaps")
	flags.BoolVarP(&s.onlyAgents, "agents", "a", false, "with installed agents only")
	flags.BoolVar(&s.debug, "debug", false, "include debugging information")
	flags.StringVarP(&s.namespace, "namespace", "n", "", "If present, the namespace scope for this CLI request")

	flags.BoolP("only-interceptable", "o", false, "")
	of := flags.Lookup("only-interceptable")
	of.Hidden = true
	of.Deprecated = "Redundant since all workloads are eligible for ingest, intercept, or replace"

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
		userD := daemon.MustGetUserClient(ctx)
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
	defer progress.Stop(cmd.Context())
	stdout := cmd.OutOrStdout()
	ctx := cmd.Context()
	userD := daemon.MustGetUserClient(ctx)
	filter := connector.ListRequest_UNSPECIFIED
	for i := range s.inclusions {
		if s.inclusions[i] {
			switch i {
			case includeIntercepts:
				filter |= connector.ListRequest_INTERCEPTS
			case includeReplacements:
				filter |= connector.ListRequest_REPLACEMENTS
			case includeIngests:
				filter |= connector.ListRequest_INGESTS
			case includeWiretaps:
				filter |= connector.ListRequest_WIRETAPS
			}
		}
	}
	if filter == connector.ListRequest_UNSPECIFIED && s.onlyAgents {
		filter = connector.ListRequest_INSTALLED_AGENTS
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
			return tpGrpc.FromGRPC(err)
		}
		s.printList(ctx, r.Workloads, stdout, formattedOutput)
		return nil
	}

	stream, streamErr := userD.WatchWorkloads(ctx, &connector.WatchWorkloadsRequest{Namespaces: []string{s.namespace}}, grpc.MaxCallRecvMsgSize(int(maxRecSize)))
	if streamErr != nil {
		return tpGrpc.FromGRPC(streamErr)
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
			ioutil.Println(stdout, "No Workloads (Deployments, StatefulSets, ReplicaSets, or Rollouts)")
		}
		return
	}

	state := func(workload *connector.WorkloadInfo) string {
		if iis, igs := workload.InterceptInfo, workload.IngestInfo; len(iis)+len(igs) > 0 {
			return intercept.DescribeIntercepts(ctx, iis, igs, nil, s.debug)
		}
		if workload.NotInterceptableReason == "Progressing" {
			return "progressing..."
		}
		if workload.AgentVersion != "" {
			return "ready to engage (traffic-agent already installed)"
		}
		if workload.NotInterceptableReason != "" {
			return "unable to engage (traffic-agent not installed): " + workload.NotInterceptableReason
		} else {
			return "ready to engage (traffic-agent not yet installed)"
		}
	}

	if formattedOut {
		output.Object(ctx, workloads, false)
	} else {
		includeNs := false
		ns := s.namespace
		for _, dep := range workloads {
			depNs := dep.Namespace
			if ns != "" && depNs != ns {
				includeNs = true
				break
			}
			ns = depNs
		}
		typeLen := 0

		nameLen := 0
		for _, dep := range workloads {
			n := dep.WorkloadResourceType
			nl := len(n)
			if nl > typeLen {
				typeLen = nl
			}
			n = dep.Name
			nl = len(n)
			if includeNs {
				nl += len(dep.Namespace) + 1
			}
			if nl > nameLen {
				nameLen = nl
			}
		}
		for _, workload := range workloads {
			t := workload.WorkloadResourceType
			n := workload.Name
			if includeNs {
				n += "." + workload.Namespace
			}
			ioutil.Printf(stdout, "%-*s %-*s: %s\n", typeLen, strings.ToLower(t), nameLen, n, state(workload))
		}
	}
}
