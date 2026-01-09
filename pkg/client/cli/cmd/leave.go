package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/progress"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc"
)

func leaveCmd() *cobra.Command {
	var containerName string
	cmd := &cobra.Command{
		Use:  "leave [flags] <intercept_name>",
		Args: cobra.ExactArgs(1),

		Short: "Remove existing intercept",
		Annotations: map[string]string{
			ann.Session: ann.Required,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := connect.InitCommand(cmd); err != nil {
				return err
			}
			defer progress.Stop(cmd.Context())
			return disengage(cmd.Context(), strings.TrimSpace(args[0]), containerName)
		},
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			shellCompDir := cobra.ShellCompDirectiveNoFileComp
			if len(args) != 0 {
				return nil, shellCompDir
			}
			if err := connect.InitCommand(cmd); err != nil {
				return nil, shellCompDir | cobra.ShellCompDirectiveError
			}
			ctx := cmd.Context()
			userD := daemon.MustGetUserClient(ctx)
			resp, err := userD.List(ctx, &connector.ListRequest{
				Filter: connector.ListRequest_INTERCEPTS | connector.ListRequest_REPLACEMENTS | connector.ListRequest_INGESTS,
			})
			if err != nil {
				return nil, shellCompDir | cobra.ShellCompDirectiveError
			}
			if len(resp.Workloads) == 0 {
				return nil, shellCompDir
			}

			var completions []string
			for _, wl := range resp.Workloads {
				for _, ii := range wl.InterceptInfo {
					name := ii.Spec.Name
					if strings.HasPrefix(name, toComplete) {
						completions = append(completions, name)
					}
				}
				for _, ig := range wl.IngestInfo {
					name := ig.Workload
					if strings.HasPrefix(name, toComplete) {
						completions = append(completions, name)
					}
				}
			}
			return completions, shellCompDir
		},
	}
	cmd.Flags().StringVarP(&containerName, "container", "c", "", "Container name")
	return cmd
}

func disengage(ctx context.Context, name, container string) error {
	userD := daemon.MustGetUserClient(ctx)

	var ic *manager.InterceptInfo
	var ig *connector.IngestInfo
	var env map[string]string
	var err error
	icName := name
	if container != "" {
		icName += "/" + container
	}
	ic, err = userD.GetIntercept(ctx, &manager.GetInterceptRequest{Name: icName})
	if err != nil && status.Code(err) != codes.NotFound {
		return grpc.FromGRPC(err)
	}

	if ic == nil {
		ig, err = userD.GetIngest(ctx, &connector.IngestIdentifier{
			WorkloadName:  name,
			ContainerName: container,
		})
		if err != nil {
			if status.Code(err) != codes.NotFound {
				return grpc.FromGRPC(err)
			}

			// User probably misspelled the name of the replace/intercept/ingest
			msg := fmt.Sprintf("Found no replace, intercept, or ingest named %q", name)
			if container != "" {
				msg = fmt.Sprintf("%s with container %q", msg, container)
			}
			return errcat.User.New(msg)
		}
		env = ig.Environment
	} else {
		env = ic.Environment
	}

	if userD.DaemonID().Containerized {
		handlerContainer, stopContainer := env["TELEPRESENCE_HANDLER_CONTAINER_NAME"]
		if stopContainer {
			// Stop the handler's container. The daemon is most likely running in another
			// container, and won't be able to.
			err = docker.StopContainer(ctx, handlerContainer)
			if err != nil {
				clog.Error(ctx, err)
			}
		}
	}

	if ic != nil {
		_, err = userD.RemoveIntercept(ctx, &manager.RemoveInterceptRequest2{Name: ic.Spec.Name})
	} else {
		_, err = userD.LeaveIngest(ctx, &connector.IngestIdentifier{
			WorkloadName:  ig.Workload,
			ContainerName: ig.Container,
		})
	}
	return grpc.FromGRPC(err)
}
