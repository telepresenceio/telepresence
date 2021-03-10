package main

import (
	"context"
	"fmt"
	"os"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli"
)

func main() {
	ctx := context.Background()
	cmd := cli.Command(ctx)
	if err := cmd.ExecuteContext(ctx); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s: error: %v\n", cmd.CommandPath(), err)
		os.Exit(1)
	}
}
