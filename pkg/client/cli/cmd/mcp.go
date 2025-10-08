package cmd

import (
	"github.com/njayp/ophis"
	"github.com/spf13/cobra"
)

func mcp() *cobra.Command {
	return ophis.Command(&ophis.Config{
		Selectors: []ophis.Selector{
			{
				CmdSelector: ophis.AllowCmds(
					"telepresence connect",
					"telepresence intercept",
					"telepresence ingest",
					"telepresence leave",
					"telepresence list",
					"telepresence quit",
					"telepresence status",
				),
			},
		},
	})
}
