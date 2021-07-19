package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

func main() {
	ctx := context.Background()
	if dir := os.Getenv("DEV_TELEPRESENCE_CONFIG_DIR"); dir != "" {
		ctx = filelocation.WithAppUserConfigDir(ctx, dir)
	}
	if dir := os.Getenv("DEV_TELEPRESENCE_LOG_DIR"); dir != "" {
		ctx = filelocation.WithAppUserLogDir(ctx, dir)
	}

	var cmd *cobra.Command
	if len(os.Args) > 1 && os.Args[1] == "daemon-foreground" || len(os.Args) > 2 && os.Args[2] == "daemon-foreground" && os.Args[1] == "help" {
		// Avoid the initialization of all subcommands except for daemon-foreground.
		cmd = cli.RootDaemonCommand(ctx)
	} else {
		cmd = cli.Command(ctx)
	}

	if err := cmd.ExecuteContext(ctx); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s: error: %v\n", cmd.CommandPath(), err)
		os.Exit(1)
	}
}
