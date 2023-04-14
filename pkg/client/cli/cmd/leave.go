package cmd

import (
	"context"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/intercept"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker"
)

func leave() *cobra.Command {
	return &cobra.Command{
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
			return removeIntercept(cmd.Context(), strings.TrimSpace(args[0]))
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
			userD := daemon.GetUserClient(ctx)
			resp, err := userD.List(ctx, &connector.ListRequest{Filter: connector.ListRequest_INTERCEPTS})
			if err != nil {
				return nil, shellCompDir | cobra.ShellCompDirectiveError
			}
			if len(resp.Workloads) == 0 {
				return nil, shellCompDir
			}

			var completions []string
			for _, intercept := range resp.Workloads {
				for _, ii := range intercept.InterceptInfos {
					name := ii.Spec.Name
					if strings.HasPrefix(name, toComplete) {
						completions = append(completions, name)
					}
				}
			}
			return completions, shellCompDir
		},
	}
}

func removeIntercept(ctx context.Context, name string) error {
	userD := daemon.GetUserClient(ctx)
	ic, err := userD.GetIntercept(ctx, &manager.GetInterceptRequest{Name: name})
	if err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
			// Obviously not there. That's ok.
			return nil
		}
		return err
	}
	if handlerContainer, ok := ic.Environment["TELEPRESENCE_HANDLER_CONTAINER_NAME"]; ok {
		// Stop the intercept handler's container. The daemon is most likely running in another
		// container, and won't be able to.
		if err = docker.StopContainer(ctx, handlerContainer); err != nil {
			dlog.Error(ctx, err)
		}
	}
	return intercept.Result(userD.RemoveIntercept(ctx, &manager.RemoveInterceptRequest2{Name: name}))
}
