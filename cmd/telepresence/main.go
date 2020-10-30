package main

import (
	"os"

	"github.com/datawire/telepresence2/pkg/client"
	"github.com/datawire/telepresence2/pkg/client/cli"
)

func main() {
	client.SetVersion(Version())
	cmd := cli.Command()
	AddVersionCommand(cmd)
	err := cmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}
