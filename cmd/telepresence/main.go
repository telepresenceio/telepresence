package main

import (
	"context"
	"fmt"
	"os"

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

	cmd := cli.Command(ctx)
	if err := cmd.ExecuteContext(ctx); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s: error: %v\n", cmd.CommandPath(), err)
		os.Exit(1)
	}
}
