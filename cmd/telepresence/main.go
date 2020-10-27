package main

import (
	"os"

	"github.com/datawire/telepresence2/pkg/client"
	"github.com/datawire/telepresence2/pkg/client/cli"
)

// Version is inserted at build using --ldflags -X
var Version = "(unknown version)"

func main() {
	client.SetVersion(Version)
	err := cli.Command().Execute()
	if err != nil {
		os.Exit(1)
	}
}
