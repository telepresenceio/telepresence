package commands

import (
	"github.com/spf13/cobra"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
)

func GetCommands() cliutil.CommandGroups {
	return map[string][]*cobra.Command{}
}
