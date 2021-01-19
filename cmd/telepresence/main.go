package main

import (
	"context"
	"fmt"
	"os"

	"github.com/datawire/telepresence2/pkg/client/cli"
)

func main() {
	ctx := context.Background()
	cmd := cli.Command()
	if err := cmd.ExecuteContext(ctx); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s: error: %v\n", cmd.CommandPath(), err)
		os.Exit(1)
	}
}
