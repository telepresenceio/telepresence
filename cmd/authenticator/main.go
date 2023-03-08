package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/authenticator"
)

func main() {
	cmd := &cobra.Command{
		Use:  "authenticator <contextName> <address>",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := authenticateContext(cmd.Context(), args[0], args[1]); err != nil {
				return fmt.Errorf("failed to authenticate context %s: %w", args[0], err)
			}
			return nil
		},
		SilenceUsage: true,
	}
	if err := cmd.Execute(); err != nil {
		log.Fatal(err)
	}
}

func authenticateContext(ctx context.Context, contextName, serverAddr string) error {
	conn, err := grpc.Dial(serverAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("failed to dial GRPC server: %w", err)
	}
	defer conn.Close()
	client := rpc.NewAuthenticatorClient(conn)

	res, err := client.GetContextExecCredentials(ctx, &rpc.GetContextExecCredentialsRequest{ContextName: contextName})
	if err != nil {
		return fmt.Errorf("failed to get exec credentials: %w", err)
	}

	_, err = fmt.Fprint(os.Stdout, string(res.RawCredentials))
	if err != nil {
		return fmt.Errorf("failed to print raw credentials: %w", err)
	}

	return nil
}
