package cmd

import (
	"context"
	"strings"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/progress"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc"
)

func revokeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:  "revoke <intercept_id>",
		Args: cobra.ExactArgs(1),

		Short: "Revoke an intercept by intercept ID. The intercept ID must be in the format <session_id>:<intercept_name>",
		Long: `Revoke an intercept by intercept ID. This is an administrative operation that
requires RBAC permissions to modify the "traffic-manager" configmap.`,
		Annotations: map[string]string{
			ann.Session: ann.Required,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := connect.InitCommand(cmd); err != nil {
				return err
			}
			defer progress.Stop(cmd.Context())
			return revokeIntercept(cmd.Context(), strings.TrimSpace(args[0]))
		},
	}
	return cmd
}

func revokeIntercept(ctx context.Context, interceptID string) error {
	if interceptID == "" {
		return errcat.User.New("intercept_id cannot be empty")
	}

	userD := daemon.MustGetUserClient(ctx)
	_, err := userD.RevokeIntercept(ctx, &connector.RevokeInterceptRequest{
		InterceptId: interceptID,
	})
	return grpc.FromGRPC(err)
}
