package flags

import (
	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

// NodeAgentDefault resolves the effective value of a --node-agent flag.
//
// If the flag was passed explicitly, flagValue (its parsed value) wins —
// this is what makes an explicit --node-agent=false override an enabled
// nodeAgent.enabled config default. Otherwise the client config's
// nodeAgent.enabled setting is used, mirroring how intercept.defaultPort
// supplies a flag default (see intercept.Command.Validate).
func NodeAgentDefault(cmd *cobra.Command, flagValue bool) bool {
	if cmd.Flags().Changed("node-agent") {
		return flagValue
	}
	return client.GetConfig(cmd.Context()).NodeAgent().Enabled
}
