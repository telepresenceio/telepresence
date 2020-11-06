package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/datawire/telepresence2/pkg/client"
	"github.com/datawire/telepresence2/pkg/client/cli"
)

func main() {
	cmd := cli.Command()
	client.AddVersionCommand(cmd)
	err := cmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}
