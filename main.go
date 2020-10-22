package main

import (
	"os"

	"github.com/datawire/telepresence2/pkg/client"
	"github.com/datawire/telepresence2/pkg/common"
)

// Version is inserted at build using --ldflags -X
var Version = "(unknown version)"

func main() {
	common.SetVersion(Version)
	err := client.Command().Execute()
	if err != nil {
		os.Exit(1)
	}
}
