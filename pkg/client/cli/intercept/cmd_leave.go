package intercept

import (
	"context"
	"strings"

	"github.com/spf13/cobra"

	"github.com/datawire/dlib/dcontext"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
)

func LeaveCommand() *cobra.Command {
	return &cobra.Command{
		Use:  "leave [flags] <intercept_name>",
		Args: cobra.ExactArgs(1),

		Short: "Remove existing intercept",
		Annotations: map[string]string{
			ann.Session: ann.Required,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cliutil.InitCommand(cmd); err != nil {
				return err
			}
			return removeIntercept(cmd.Context(), strings.TrimSpace(args[0]))
		},
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			shellCompDir := cobra.ShellCompDirectiveNoFileComp
			if len(args) != 0 {
				return nil, shellCompDir
			}
			if err := cliutil.InitCommand(cmd); err != nil {
				return nil, shellCompDir | cobra.ShellCompDirectiveError
			}
			ctx := cmd.Context()
			userD := cliutil.GetUserDaemon(ctx)
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
	userD := cliutil.GetUserDaemon(ctx)
	return Result(userD.RemoveIntercept(dcontext.WithoutCancel(ctx), &manager.RemoveInterceptRequest2{Name: name}))
}
