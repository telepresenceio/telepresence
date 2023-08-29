package cmd

import (
	"context"
	"fmt"
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
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
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
			// User probably misspelled the name of the intercept
			return errcat.User.Newf("Intercept named %q not found", name)
		}
		return err
	}
	handlerContainer, stopContainer := ic.Environment["TELEPRESENCE_HANDLER_CONTAINER_NAME"]
	if stopContainer {
		// Stop the intercept handler's container. The daemon is most likely running in another
		// container, and won't be able to.
		err = docker.StopContainer(docker.EnableClient(ctx), handlerContainer)
		if err != nil {
			dlog.Error(ctx, err)
		}
	}
	if err := intercept.Result(userD.RemoveIntercept(ctx, &manager.RemoveInterceptRequest2{Name: name})); err != nil {
		if stopContainer && strings.Contains(err.Error(), fmt.Sprintf("%q not found", name)) {
			// race condition between stopping the intercept handler, which causes the intercept to leave, and this call
			err = nil
		}
	}
	return err
}
