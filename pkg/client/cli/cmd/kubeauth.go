package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/authenticator"
)

func kubeauthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "kubeauth",
		Args:   cobra.ExactArgs(2),
		Short:  "Resolve kubeconfig context using gRPC to kubeauth server",
		RunE:   authenticateContext,
		Hidden: true,
	}
	return cmd
}

func authenticateContext(cmd *cobra.Command, args []string) (err error) {
	ctx := cmd.Context()
	contextName := args[0]
	serverAddr := args[1]
	defer func() {
		if err != nil {
			dlog.Error(ctx, err)
		}
	}()
	var conn *grpc.ClientConn
	if conn, err = grpc.Dial(serverAddr, grpc.WithTransportCredentials(insecure.NewCredentials())); err != nil {
		return fmt.Errorf("failed to dial GRPC server %s: %w", serverAddr, err)
	}
	defer conn.Close()

	ac := rpc.NewAuthenticatorClient(conn)
	var res *rpc.GetContextExecCredentialsResponse
	if res, err = ac.GetContextExecCredentials(ctx, &rpc.GetContextExecCredentialsRequest{ContextName: contextName}); err != nil {
		return fmt.Errorf("failed to get exec credentials: %w", err)
	}
	if _, err = os.Stdout.Write(res.RawCredentials); err != nil {
		err = fmt.Errorf("failed to print raw credentials: %w", err)
	}
	return err
}
